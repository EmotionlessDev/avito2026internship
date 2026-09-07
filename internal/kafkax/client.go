package kafkax

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/config"
	"github.com/emotionlessdev/avito-kitchen/internal/outbox"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Producer публикует сообщения в Kafka и реализует outbox.Publisher.
type Producer struct {
	client *kgo.Client
}

// NewProducer поднимает продюсера.
func NewProducer(cfg config.Kafka) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		// Идемпотентный продюсер включён по умолчанию в franz-go: повтор
		// внутри одной сессии не задваивает сообщение в партиции.
		kgo.ProducerBatchMaxBytes(cfg.MaxMessageBytes),
		kgo.RequestTimeoutOverhead(cfg.Timeout),
	)
	if err != nil {
		return nil, fmt.Errorf("продюсер Kafka: %w", err)
	}

	return &Producer{client: client}, nil
}

// Publish отправляет пачку синхронно: relay обязан узнать результат до того,
// как отметит событие опубликованным.
func (p *Producer) Publish(ctx context.Context, messages []outbox.Message) error {
	records := make([]*kgo.Record, 0, len(messages))

	for i := range messages {
		records = append(records, record(messages[i]))
	}

	if err := p.client.ProduceSync(ctx, records...).FirstErr(); err != nil {
		return fmt.Errorf("публикация в Kafka: %w", err)
	}

	return nil
}

// Close закрывает продюсера, дожидаясь отправки буферов.
func (p *Producer) Close() {
	p.client.Close()
}

func record(message outbox.Message) *kgo.Record {
	headers := make([]kgo.RecordHeader, 0, len(message.Headers))
	for key, value := range message.Headers {
		headers = append(headers, kgo.RecordHeader{Key: key, Value: []byte(value)})
	}

	return &kgo.Record{
		Topic:   message.Topic,
		Key:     message.Key,
		Value:   message.Value,
		Headers: headers,
	}
}

// EnsureTopics создаёт топики, если их ещё нет.
//
// Автосоздание топиков в брокере выключено осознанно: опечатка в имени
// не должна порождать новый топик. Поэтому нужные топики заводит воркер
// на старте — операция идемпотентна, повторный запуск ничего не ломает.
func EnsureTopics(ctx context.Context, cfg config.Kafka, log *slog.Logger) error {
	client, err := kgo.NewClient(kgo.SeedBrokers(cfg.Brokers...))
	if err != nil {
		return fmt.Errorf("админ-клиент Kafka: %w", err)
	}

	defer client.Close()

	admin := kadm.NewClient(client)

	// Компактируемый топик каталога: по ключу остаётся последний снимок
	// позиции, и витрину поиска можно пересобрать с нуля (docs/DESIGN.md §5).
	compacted := "compact"

	topics := map[string]map[string]*string{
		TopicCatalogEvents: {"cleanup.policy": &compacted},
		TopicOrderEvents:   {},
		TopicDeadLetter:    {},
	}

	for name, configs := range topics {
		responses, err := admin.CreateTopics(ctx, cfg.Partitions, cfg.ReplicationFactor, configs, name)
		if err != nil {
			return fmt.Errorf("создание топика %s: %w", name, err)
		}

		for _, response := range responses {
			if response.Err == nil {
				log.InfoContext(ctx, "топик создан", slog.String("topic", response.Topic))

				continue
			}

			// Топик уже есть — штатный исход при повторном старте воркера.
			if isTopicExists(response.Err) {
				continue
			}

			return fmt.Errorf("создание топика %s: %w", response.Topic, response.Err)
		}
	}

	return nil
}

// isTopicExists отличает «топик уже создан» от настоящей ошибки: первое
// при повторном старте нормально, второе обязано валить запуск.
func isTopicExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "TOPIC_ALREADY_EXISTS")
}

// Route отправляет событие в топик по типу агрегата.
func Route(aggregateType, _ string) string {
	if aggregateType == "order" {
		return TopicOrderEvents
	}

	return TopicCatalogEvents
}

// WaitReady ждёт, пока брокер начнёт отвечать: воркер стартует рядом
// с Kafka, и первые секунды она может быть ещё не готова.
func WaitReady(ctx context.Context, cfg config.Kafka, log *slog.Logger) error {
	client, err := kgo.NewClient(kgo.SeedBrokers(cfg.Brokers...))
	if err != nil {
		return fmt.Errorf("клиент Kafka: %w", err)
	}

	defer client.Close()

	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		err := client.Ping(pingCtx)
		cancel()

		if err == nil {
			return nil
		}

		if ctx.Err() != nil {
			return fmt.Errorf("ожидание Kafka прервано: %w", ctx.Err())
		}

		log.WarnContext(ctx, "Kafka ещё не отвечает",
			slog.Int("attempt", attempt),
			slog.String("error", err.Error()))

		select {
		case <-ctx.Done():
			return fmt.Errorf("ожидание Kafka прервано: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
