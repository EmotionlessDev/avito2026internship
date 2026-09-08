package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/events"
	"github.com/emotionlessdev/avito-kitchen/internal/kafkax"
	"github.com/emotionlessdev/avito-kitchen/internal/outbox"
	"github.com/emotionlessdev/avito-kitchen/internal/search"
	"github.com/emotionlessdev/avito-kitchen/internal/stats"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingPublisher — брокер, который всё принимает и всё запоминает.
// Kafka в этих тестах не нужна: проверяется договор relay с базой,
// а не сеть.
type recordingPublisher struct {
	mu       sync.Mutex
	messages []outbox.Message
	fail     error
}

func (p *recordingPublisher) Publish(_ context.Context, messages []outbox.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.fail != nil {
		return p.fail
	}

	p.messages = append(p.messages, messages...)

	return nil
}

func (p *recordingPublisher) collected() []outbox.Message {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]outbox.Message(nil), p.messages...)
}

func writeOutbox(t *testing.T, event outbox.Event) {
	t.Helper()

	ctx := testContext(t)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback(ctx) }()

	require.NoError(t, outbox.Write(ctx, tx, event))
	require.NoError(t, tx.Commit(ctx))
}

func runRelay(t *testing.T, publisher outbox.Publisher) {
	t.Helper()

	ctx, cancel := context.WithCancel(testContext(t))
	relay := outbox.NewRelay(pool, publisher,
		func(aggregateType, eventType string) string { return "test-" + aggregateType },
		"dead-letter",
		outbox.Config{BatchSize: 50, PollInterval: 100 * time.Millisecond, MaxAttempts: 2},
		quietLogger())

	done := make(chan struct{})

	go func() {
		defer close(done)

		if err := relay.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("relay завершился ошибкой: %v", err)
		}
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// Relay забирает неопубликованное, отправляет в брокер и помечает строку.
// Ключ партиции — aggregate_id: порядок событий одного заказа обязан
// сохраняться, а глобальный порядок не нужен.
func TestOutboxRelayPublishesAndMarks(t *testing.T) {
	ctx := testContext(t)
	publisher := &recordingPublisher{}

	aggregate := uuid.New()
	writeOutbox(t, outbox.Event{
		AggregateType: events.AggregateOrder,
		AggregateID:   aggregate,
		Type:          events.TypeOrderCreated,
		Payload:       map[string]string{"number": "К-260907-9999"},
	})

	runRelay(t, publisher)

	require.Eventually(t, func() bool {
		var unpublished int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM outbox WHERE aggregate_id = $1 AND published_at IS NULL`,
			aggregate).Scan(&unpublished); err != nil {
			return false
		}

		return unpublished == 0
	}, 10*time.Second, 100*time.Millisecond, "relay не опубликовал событие")

	var found bool

	for _, message := range publisher.collected() {
		if string(message.Key) == aggregate.String() {
			found = true

			assert.Equal(t, "test-order", message.Topic)
			assert.NotEmpty(t, message.Headers["event_id"], "заголовок дедупликации обязателен")
		}
	}

	assert.True(t, found, "событие не доехало до брокера")
}

// Отказ брокера не теряет событие: строка остаётся неопубликованной,
// счётчик попыток растёт, а после исчерпания попыток событие уезжает в DLQ —
// одно отложенное сообщение лучше остановленного потока.
func TestOutboxRelayMovesToDeadLetterAfterMaxAttempts(t *testing.T) {
	ctx := testContext(t)
	publisher := &recordingPublisher{fail: errors.New("брокер отказал")}

	aggregate := uuid.New()
	writeOutbox(t, outbox.Event{
		AggregateType: events.AggregateOrder,
		AggregateID:   aggregate,
		Type:          events.TypeOrderCreated,
		Payload:       map[string]string{"number": "К-260907-8888"},
	})

	runRelay(t, publisher)

	require.Eventually(t, func() bool {
		var attempts int
		if err := pool.QueryRow(ctx,
			`SELECT coalesce(max(attempts), 0) FROM outbox WHERE aggregate_id = $1`,
			aggregate).Scan(&attempts); err != nil {
			return false
		}

		return attempts >= 1
	}, 10*time.Second, 100*time.Millisecond, "счётчик попыток не вырос")

	var lastError *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT last_error FROM outbox WHERE aggregate_id = $1`, aggregate).Scan(&lastError))
	require.NotNil(t, lastError)
	assert.Contains(t, *lastError, "брокер отказал")
}

// Дедупликация приёма: один и тот же event_id второй раз не применяется.
func TestProcessedEventsDeduplicate(t *testing.T) {
	ctx := testContext(t)
	eventID := uuid.New()

	inTx(t, func(tx pgx.Tx) {
		fresh, err := outbox.MarkProcessed(ctx, tx, "тест-консьюмер", eventID)
		require.NoError(t, err)
		assert.True(t, fresh, "первое применение — новое")
	})

	inTx(t, func(tx pgx.Tx) {
		fresh, err := outbox.MarkProcessed(ctx, tx, "тест-консьюмер", eventID)
		require.NoError(t, err)
		assert.False(t, fresh, "повтор обязан быть распознан")
	})

	inTx(t, func(tx pgx.Tx) {
		fresh, err := outbox.MarkProcessed(ctx, tx, "другой-консьюмер", eventID)
		require.NoError(t, err)
		assert.True(t, fresh, "у каждого консьюмера свой счёт применённого")
	})
}

func inTx(t *testing.T, do func(tx pgx.Tx)) {
	t.Helper()

	ctx := testContext(t)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback(ctx) }()

	do(tx)

	require.NoError(t, tx.Commit(ctx))
}

func catalogRecord(t *testing.T, snapshot events.MenuItemSnapshot) kafkax.Record {
	t.Helper()

	value, err := json.Marshal(snapshot)
	require.NoError(t, err)

	return kafkax.Record{
		Topic:   kafkax.TopicCatalogEvents,
		Key:     []byte(snapshot.ItemID.String()),
		Value:   value,
		EventID: uuid.New(),
		Type:    events.TypeMenuItemChanged,
	}
}

// Индексатор строит витрину только из события-снимка: в меню он не ходит.
// Поэтому его можно кормить событиями позиции, которой в каталоге нет.
func TestSearchIndexerUpsertsFromSnapshot(t *testing.T) {
	ctx := testContext(t)
	indexer := search.NewIndexer(pool, quietLogger())
	repo := search.NewRepository(pool)

	itemID := uuid.New()
	snapshot := events.MenuItemSnapshot{
		ItemID:           itemID,
		VenueID:          demoVenue,
		ItemName:         "Чебурек с бараниной",
		Description:      "Тестовая позиция витрины",
		VenueName:        "Хачапури и вино",
		CategoryName:     "Тесты",
		PriceMinor:       33000,
		IsAvailable:      true,
		VenueIsActive:    true,
		VenueIsAccepting: true,
		ChangedAt:        time.Now().UTC(),
	}

	t.Cleanup(func() {
		_, err := pool.Exec(testContext(t), `DELETE FROM search_items WHERE item_id = $1`, itemID)
		require.NoError(t, err)
	})

	require.NoError(t, indexer.Handle(ctx, catalogRecord(t, snapshot)))

	found, err := repo.FullText(ctx, "чебурек", 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, itemID, found[0].ItemID)
	assert.True(t, found[0].IsAvailable)

	// Стоп-лист не выбрасывает позицию из выдачи, а помечает её.
	snapshot.IsAvailable = false
	require.NoError(t, indexer.Handle(ctx, catalogRecord(t, snapshot)))

	stopped, err := repo.FullText(ctx, "чебурек", 10)
	require.NoError(t, err)
	require.Len(t, stopped, 1)
	assert.False(t, stopped[0].IsAvailable)

	// Архивная позиция исчезает из витрины совсем.
	snapshot.IsArchived = true
	require.NoError(t, indexer.Handle(ctx, catalogRecord(t, snapshot)))

	gone, err := repo.FullText(ctx, "чебурек", 10)
	require.NoError(t, err)
	assert.Empty(t, gone)
}

// Опечатка ловится триграммами: полнотекст «хачапур» не находит, similarity —
// находит. Ровно это и есть сценарий U12.
func TestSearchTrigramFindsTypo(t *testing.T) {
	ctx := testContext(t)
	repo := search.NewRepository(pool)

	exact, err := repo.FullText(ctx, "хачапури", 10)
	require.NoError(t, err)
	require.NotEmpty(t, exact, "точный запрос обязан находиться полнотекстом")

	typo, err := repo.Trigram(ctx, "хачапур", 10)
	require.NoError(t, err)
	require.NotEmpty(t, typo, "опечатка обязана находиться по триграммам")

	names := make([]string, 0, len(typo))
	for _, item := range typo {
		names = append(names, item.ItemName)
	}

	assert.Contains(t, names, "Хачапури по-аджарски")
}

// Витрина статистики — инкрементальная проекция, поэтому дедупликация
// обязательна: повторная доставка не должна накрутить лишний заказ.
func TestStatsProjectorIsIdempotent(t *testing.T) {
	ctx := testContext(t)
	projector := stats.NewProjector(pool, quietLogger())

	day := time.Now().UTC()

	before := dailyStats(t, demoVenue)

	created, err := json.Marshal(events.OrderCreated{
		OrderID: uuid.New(), Number: "К-260907-7777", UserID: demoUser,
		VenueID: demoVenue, VenueName: "Хачапури и вино",
		ItemsCount: 1, TotalAmountMinor: 50000, CreatedAt: day,
	})
	require.NoError(t, err)

	record := kafkax.Record{
		Topic: kafkax.TopicOrderEvents, Value: created,
		EventID: uuid.New(), Type: events.TypeOrderCreated,
	}

	require.NoError(t, projector.Handle(ctx, record))
	require.NoError(t, projector.Handle(ctx, record), "повтор обязан быть безопасным")

	after := dailyStats(t, demoVenue)
	assert.Equal(t, before.total+1, after.total, "повторная доставка не должна удваивать счётчик")

	placed := day.Add(-46 * time.Second)
	completed, err := json.Marshal(events.OrderStatusChanged{
		OrderID: uuid.New(), Number: "К-260907-7777", UserID: demoUser, VenueID: demoVenue,
		FromStatus: "delivering", ToStatus: "completed", ActorType: "system",
		ChangedAt: day, TotalAmountMinor: 50000, OrderCreatedAt: day, PlacedAt: &placed,
	})
	require.NoError(t, err)

	require.NoError(t, projector.Handle(ctx, kafkax.Record{
		Topic: kafkax.TopicOrderEvents, Value: completed,
		EventID: uuid.New(), Type: events.TypeOrderStatusChanged,
	}))

	final := dailyStats(t, demoVenue)
	assert.Equal(t, after.completed+1, final.completed)
	assert.Equal(t, after.revenue+50000, final.revenue)
}

type dayCounters struct {
	total     int
	completed int
	revenue   int64
}

func dailyStats(t *testing.T, venueID uuid.UUID) dayCounters {
	t.Helper()

	var counters dayCounters

	err := pool.QueryRow(testContext(t), `
		SELECT coalesce(orders_total, 0), coalesce(orders_completed, 0), coalesce(revenue_minor, 0)
		FROM venue_daily_stats
		WHERE venue_id = $1 AND stat_date = (now() AT TIME ZONE 'Europe/Moscow')::date`, venueID).
		Scan(&counters.total, &counters.completed, &counters.revenue)

	if errors.Is(err, pgx.ErrNoRows) {
		return dayCounters{}
	}

	require.NoError(t, err)

	return counters
}
