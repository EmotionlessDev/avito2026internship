package kafkax

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/emotionlessdev/avito-kitchen/internal/config"
	"github.com/emotionlessdev/avito-kitchen/internal/outbox"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Record — сообщение, как его видит обработчик.
type Record struct {
	Topic string
	Key   []byte
	Value []byte
	// EventID — ключ дедупликации. Приезжает заголовком, который проставил
	// relay; без него сообщение считается неопознанным и уезжает в DLQ.
	EventID uuid.UUID
	Type    string
}

// Handler применяет событие к своей проекции.
//
// Обработчик обязан быть идемпотентным: at-least-once у Kafka — норма,
// и одно и то же событие может прийти дважды (docs/DESIGN.md §5).
type Handler interface {
	Handle(ctx context.Context, record Record) error
}

// Consumer читает топики группой и отдаёт сообщения обработчику.
type Consumer struct {
	client    *kgo.Client
	group     string
	handler   Handler
	publisher outbox.Publisher
	log       *slog.Logger
}

// NewConsumer собирает консьюмера группы.
func NewConsumer(
	cfg config.Kafka, group string, topics []string,
	handler Handler, publisher outbox.Publisher, log *slog.Logger,
) (*Consumer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		// Смещения коммитим сами, после успешной обработки: автокоммит
		// подтвердил бы сообщение, которое ещё не доехало до проекции.
		kgo.DisableAutoCommit(),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return nil, fmt.Errorf("консьюмер %s: %w", group, err)
	}

	return &Consumer{
		client:    client,
		group:     group,
		handler:   handler,
		publisher: publisher,
		log:       log.With(slog.String("consumer", group)),
	}, nil
}

// Run читает сообщения до отмены контекста.
func (c *Consumer) Run(ctx context.Context) error {
	defer c.client.Close()

	c.log.InfoContext(ctx, "консьюмер запущен")

	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "консьюмер остановлен")

			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, err := range errs {
				c.log.ErrorContext(ctx, "ошибка чтения из Kafka",
					slog.String("topic", err.Topic),
					slog.String("error", err.Err.Error()))
			}

			continue
		}

		fetches.EachRecord(func(kr *kgo.Record) {
			c.process(ctx, kr)
		})

		if err := c.client.CommitUncommittedOffsets(ctx); err != nil && ctx.Err() == nil {
			c.log.ErrorContext(ctx, "не удалось зафиксировать смещения",
				slog.String("error", err.Error()))
		}
	}
}

// process применяет одно сообщение. Ошибка обработки не останавливает поток:
// сообщение уезжает в DLQ, а группа идёт дальше — иначе одно ядовитое
// событие подвесило бы всю проекцию.
func (c *Consumer) process(ctx context.Context, kr *kgo.Record) {
	record := toRecord(kr)

	if record.EventID == uuid.Nil {
		c.log.ErrorContext(ctx, "сообщение без event_id уходит в DLQ",
			slog.String("topic", kr.Topic))
		c.toDeadLetter(ctx, kr, "нет заголовка event_id")

		return
	}

	if err := c.handler.Handle(ctx, record); err != nil {
		if ctx.Err() != nil {
			return
		}

		c.log.ErrorContext(ctx, "событие не применено, уходит в DLQ",
			slog.String("event_id", record.EventID.String()),
			slog.String("event_type", record.Type),
			slog.String("error", err.Error()))

		c.toDeadLetter(ctx, kr, err.Error())
	}
}

func (c *Consumer) toDeadLetter(ctx context.Context, kr *kgo.Record, reason string) {
	if c.publisher == nil {
		return
	}

	headers := map[string]string{
		"origin_topic": kr.Topic,
		"consumer":     c.group,
		"error":        reason,
	}

	for _, header := range kr.Headers {
		headers[header.Key] = string(header.Value)
	}

	if err := c.publisher.Publish(ctx, []outbox.Message{{
		Topic:   TopicDeadLetter,
		Key:     kr.Key,
		Value:   kr.Value,
		Headers: headers,
	}}); err != nil && ctx.Err() == nil {
		c.log.ErrorContext(ctx, "не удалось увести сообщение в DLQ",
			slog.String("error", err.Error()))
	}
}

func toRecord(kr *kgo.Record) Record {
	record := Record{Topic: kr.Topic, Key: kr.Key, Value: kr.Value}

	for _, header := range kr.Headers {
		switch header.Key {
		case "event_id":
			if id, err := uuid.Parse(string(header.Value)); err == nil {
				record.EventID = id
			}
		case "event_type":
			record.Type = string(header.Value)
		}
	}

	return record
}
