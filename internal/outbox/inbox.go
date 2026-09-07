package outbox

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MarkProcessed отмечает событие обработанным и говорит, надо ли его применять.
//
// Вызывается в той же транзакции, что и проекция: тогда «применено» и «отмечено»
// становятся видимыми вместе, и повторная доставка (at-least-once у Kafka —
// норма) не приводит к двойному применению. Возвращает false, если событие
// этим консьюмером уже применялось.
func MarkProcessed(ctx context.Context, tx pgx.Tx, consumer string, eventID uuid.UUID) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO processed_events (consumer, event_id)
		VALUES ($1, $2)
		ON CONFLICT (consumer, event_id) DO NOTHING`, consumer, eventID)
	if err != nil {
		return false, fmt.Errorf("отметка обработанного события: %w", err)
	}

	return tag.RowsAffected() == 1, nil
}
