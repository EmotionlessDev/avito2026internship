package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Message — то, что relay отдаёт брокеру. Тип свой, а не из kafkax:
// outbox не должен знать, что на другом конце именно Kafka.
type Message struct {
	Topic   string
	Key     []byte
	Value   []byte
	Headers map[string]string
}

// Publisher доставляет сообщения в брокер.
type Publisher interface {
	Publish(ctx context.Context, messages []Message) error
}

// TopicRouter решает, в какой топик едет событие.
type TopicRouter func(aggregateType, eventType string) string

// NotifyChannel — канал, в который триггер базы шлёт уведомление о вставке
// (см. миграцию 0004). Он снимает задержку опроса: relay просыпается
// на записи, а не ждёт следующего тика.
const NotifyChannel = "outbox_new"

// Config — настройки relay.
type Config struct {
	// BatchSize ограничивает размер одной пачки: длинная транзакция держит
	// блокировки строк и мешает соседним воркерам.
	BatchSize int
	// PollInterval — страховка на случай, если уведомление не дошло:
	// оно не переживает разрыв соединения, а строка в таблице переживает.
	PollInterval time.Duration
	// MaxAttempts — после скольких неудач событие уезжает в DLQ.
	//
	// Считаются только явные отказы брокера: недоступность Kafka клиент
	// переживает сам, удерживая вызов, и в счётчик не попадает. Иначе
	// длинная авария увела бы в DLQ здоровые события — а туда должны
	// попадать только те, которые брокер отказывается принимать в принципе.
	MaxAttempts int
}

// Relay доставляет события из outbox в брокер.
//
// Порядок публикации глобально не гарантирован и не требуется: bigserial
// при конкурентных транзакциях не совпадает с порядком коммитов. Гарантирован
// порядок per-aggregate — ключом партиции служит aggregate_id
// (docs/DESIGN.md §5).
type Relay struct {
	pool      *pgxpool.Pool
	publisher Publisher
	router    TopicRouter
	dlqTopic  string
	cfg       Config
	log       *slog.Logger
}

// NewRelay собирает relay.
func NewRelay(
	pool *pgxpool.Pool, publisher Publisher, router TopicRouter,
	dlqTopic string, cfg Config, log *slog.Logger,
) *Relay {
	return &Relay{
		pool:      pool,
		publisher: publisher,
		router:    router,
		dlqTopic:  dlqTopic,
		cfg:       cfg,
		log:       log,
	}
}

// Run гоняет relay до отмены контекста.
func (r *Relay) Run(ctx context.Context) error {
	wake := make(chan struct{}, 1)

	go r.listen(ctx, wake)

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	r.log.InfoContext(ctx, "outbox-relay запущен",
		slog.Int("batch_size", r.cfg.BatchSize),
		slog.Duration("poll_interval", r.cfg.PollInterval))

	for {
		// Черпаем, пока есть что: одно уведомление может означать
		// целую пачку событий.
		for {
			published, err := r.drain(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}

				r.log.ErrorContext(ctx, "не удалось опубликовать пачку событий",
					slog.String("error", err.Error()))

				break
			}

			if published < r.cfg.BatchSize {
				break
			}
		}

		select {
		case <-ctx.Done():
			r.log.InfoContext(ctx, "outbox-relay остановлен")

			return nil
		case <-ticker.C:
		case <-wake:
		}
	}
}

// listen держит отдельное соединение под LISTEN. Пул для этого не годится:
// уведомления приходят в конкретное соединение, а пул волен его вернуть.
func (r *Relay) listen(ctx context.Context, wake chan<- struct{}) {
	for ctx.Err() == nil {
		if err := r.listenOnce(ctx, wake); err != nil && ctx.Err() == nil {
			r.log.WarnContext(ctx, "слушатель outbox переподключается",
				slog.String("error", err.Error()))

			select {
			case <-ctx.Done():
				return
			case <-time.After(r.cfg.PollInterval):
			}
		}
	}
}

func (r *Relay) listenOnce(ctx context.Context, wake chan<- struct{}) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("соединение для LISTEN: %w", err)
	}

	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+NotifyChannel); err != nil {
		return fmt.Errorf("LISTEN %s: %w", NotifyChannel, err)
	}

	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return fmt.Errorf("ожидание уведомления: %w", err)
		}

		select {
		case wake <- struct{}{}:
		default:
			// Сигнал уже стоит в очереди — второй ничего не добавит.
		}
	}
}

// record — строка outbox, взятая в работу.
type record struct {
	id            int64
	eventID       uuid.UUID
	aggregateType string
	aggregateID   uuid.UUID
	eventType     string
	payload       []byte
	headers       []byte
	attempts      int
}

// drain публикует одну пачку и возвращает число обработанных записей.
//
// Публикация идёт внутри транзакции: если брокер ответил, а коммит не прошёл,
// событие уедет повторно. Это осознанный выбор в пользу at-least-once —
// консьюмеры дедуплицируют по event_id, а потерянное событие вернуть неоткуда.
func (r *Relay) drain(ctx context.Context) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("начало транзакции relay: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	records, err := claim(ctx, tx, r.cfg.BatchSize)
	if err != nil {
		return 0, err
	}

	if len(records) == 0 {
		return 0, nil
	}

	messages := make([]Message, 0, len(records))
	for i := range records {
		messages = append(messages, r.message(records[i]))
	}

	if publishErr := r.publisher.Publish(ctx, messages); publishErr != nil {
		if err := r.markFailed(ctx, tx, records, publishErr); err != nil {
			return 0, err
		}

		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("коммит после неудачной публикации: %w", err)
		}

		return 0, fmt.Errorf("публикация в брокер: %w", publishErr)
	}

	if err := markPublished(ctx, tx, records); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("коммит relay: %w", err)
	}

	r.log.DebugContext(ctx, "события опубликованы", slog.Int("count", len(records)))

	return len(records), nil
}

// claim берёт неопубликованные записи под блокировку.
//
// FOR UPDATE SKIP LOCKED — то, ради чего relay может работать в нескольких
// экземплярах: каждый берёт свою порцию, никто никого не ждёт.
func claim(ctx context.Context, tx pgx.Tx, limit int) ([]record, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, event_id, aggregate_type, aggregate_id, event_type, payload, headers, attempts
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, fmt.Errorf("выборка событий: %w", err)
	}

	defer rows.Close()

	var records []record

	for rows.Next() {
		var item record

		if err := rows.Scan(
			&item.id, &item.eventID, &item.aggregateType, &item.aggregateID,
			&item.eventType, &item.payload, &item.headers, &item.attempts,
		); err != nil {
			return nil, fmt.Errorf("чтение события: %w", err)
		}

		records = append(records, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение событий: %w", err)
	}

	return records, nil
}

func (r *Relay) message(item record) Message {
	headers := map[string]string{
		"event_id":       item.eventID.String(),
		"event_type":     item.eventType,
		"aggregate_type": item.aggregateType,
	}

	var stored map[string]string
	if err := json.Unmarshal(item.headers, &stored); err == nil {
		for key, value := range stored {
			headers[key] = value
		}
	}

	return Message{
		Topic: r.router(item.aggregateType, item.eventType),
		// Ключ партиции — агрегат: события одного заказа не обгоняют
		// друг друга, а компакция каталога схлопывает историю позиции
		// до последнего снимка.
		Key:     []byte(item.aggregateID.String()),
		Value:   item.payload,
		Headers: headers,
	}
}

func markPublished(ctx context.Context, tx pgx.Tx, records []record) error {
	ids := make([]int64, 0, len(records))
	for i := range records {
		ids = append(ids, records[i].id)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE outbox SET published_at = now(), last_error = NULL WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("отметка опубликованных: %w", err)
	}

	return nil
}

// markFailed считает попытки, а исчерпавшие лимит уводит в DLQ.
//
// Одно ядовитое сообщение не должно останавливать поток: если оно не уезжает
// после MaxAttempts попыток, дальше им занимается человек, а очередь едет.
func (r *Relay) markFailed(ctx context.Context, tx pgx.Tx, records []record, cause error) error {
	var exhausted []record

	for i := range records {
		if records[i].attempts+1 >= r.cfg.MaxAttempts {
			exhausted = append(exhausted, records[i])
		}
	}

	ids := make([]int64, 0, len(records))
	for i := range records {
		ids = append(ids, records[i].id)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE outbox SET attempts = attempts + 1, last_error = $2 WHERE id = ANY($1)`,
		ids, cause.Error()); err != nil {
		return fmt.Errorf("отметка неудачной попытки: %w", err)
	}

	if len(exhausted) == 0 {
		return nil
	}

	return r.moveToDeadLetter(ctx, tx, exhausted, cause)
}

func (r *Relay) moveToDeadLetter(
	ctx context.Context, tx pgx.Tx, records []record, cause error,
) error {
	messages := make([]Message, 0, len(records))

	for i := range records {
		message := r.message(records[i])
		message.Topic = r.dlqTopic
		message.Headers["error"] = cause.Error()
		message.Headers["origin_topic"] = r.router(records[i].aggregateType, records[i].eventType)
		messages = append(messages, message)
	}

	if err := r.publisher.Publish(ctx, messages); err != nil {
		// В DLQ тоже не уехало — оставляем событие в outbox: потерять его
		// нельзя, а следующая попытка случится на следующем тике.
		r.log.ErrorContext(ctx, "не удалось увести события в DLQ",
			slog.String("error", err.Error()),
			slog.Int("count", len(records)))

		return nil
	}

	ids := make([]int64, 0, len(records))
	for i := range records {
		ids = append(ids, records[i].id)
		r.log.WarnContext(ctx, "событие уведено в DLQ",
			slog.String("event_id", records[i].eventID.String()),
			slog.String("event_type", records[i].eventType),
			slog.Int("attempts", records[i].attempts+1))
	}

	if _, err := tx.Exec(ctx,
		`UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("отметка событий, ушедших в DLQ: %w", err)
	}

	return nil
}
