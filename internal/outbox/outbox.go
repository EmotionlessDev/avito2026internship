// Package outbox — надёжная публикация событий.
//
// Событие нельзя опубликовать в Kafka «после коммита»: между COMMIT
// и Produce процесс может умереть, и факт останется незамеченным
// (docs/DESIGN.md §5). Поэтому событие пишется в ту же транзакцию, что
// и данные, а отдельный relay доставляет его в брокер.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event — запись outbox до публикации.
type Event struct {
	// ID — ключ дедупликации у консьюмера. Генерирует приложение (UUID v7).
	ID uuid.UUID
	// AggregateType и AggregateID определяют, чей это факт. AggregateID
	// служит ключом партиции Kafka, поэтому события одного заказа или одной
	// позиции меню приходят консьюмеру по порядку.
	AggregateType string
	AggregateID   uuid.UUID
	Type          string
	Payload       any
	Headers       map[string]string
}

// Write кладёт событие в outbox внутри переданной транзакции.
//
// Функция, а не метод репозитория: писать события должны все домены,
// и каждый делает это в своей транзакции. Общий репозиторий тут только
// мешал бы — он не знает про чужие транзакции.
func Write(ctx context.Context, tx pgx.Tx, event Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("сериализация события %s: %w", event.Type, err)
	}

	headers := event.Headers
	if headers == nil {
		headers = map[string]string{}
	}

	encodedHeaders, err := json.Marshal(headers)
	if err != nil {
		return fmt.Errorf("сериализация заголовков события: %w", err)
	}

	id := event.ID
	if id == uuid.Nil {
		id = newEventID()
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (event_id, aggregate_type, aggregate_id, event_type, payload, headers)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, event.AggregateType, event.AggregateID, event.Type, payload, encodedHeaders)
	if err != nil {
		return fmt.Errorf("запись события %s в outbox: %w", event.Type, err)
	}

	return nil
}

// WriteAll кладёт пачку событий одним проходом.
func WriteAll(ctx context.Context, tx pgx.Tx, events []Event) error {
	for _, event := range events {
		if err := Write(ctx, tx, event); err != nil {
			return err
		}
	}

	return nil
}

func newEventID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}

	return id
}
