package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/emotionlessdev/avito-kitchen/internal/events"
	"github.com/emotionlessdev/avito-kitchen/internal/kafkax"
	"github.com/emotionlessdev/avito-kitchen/internal/order"
)

// Starter — страховка на запуск процесса заказа.
//
// Быстрый путь — синхронный вызов из HTTP-хендлера. Если он не удался
// (движок моргнул, сеть подвела), факт оформления всё равно уехал в outbox
// и приедет сюда событием. Обе попытки идемпотентны, потому что идентификатор
// workflow выводится из заказа — дубликат процесса невозможен
// (docs/DESIGN.md §5).
type Starter struct {
	process *Process
	log     *slog.Logger
}

// NewStarter собирает потребителя, поднимающего процессы заказов.
func NewStarter(process *Process, log *slog.Logger) *Starter {
	return &Starter{process: process, log: log}
}

// Handle поднимает процесс заказа по событию оформления.
func (s *Starter) Handle(ctx context.Context, record kafkax.Record) error {
	if record.Type != events.TypeOrderCreated {
		return nil
	}

	var payload events.OrderCreated
	if err := json.Unmarshal(record.Value, &payload); err != nil {
		return fmt.Errorf("разбор события оформления: %w", err)
	}

	err := s.process.Start(ctx, order.ProcessInput{
		OrderID:          payload.OrderID,
		UserID:           payload.UserID,
		VenueID:          payload.VenueID,
		Number:           payload.Number,
		TotalAmountMinor: payload.TotalAmountMinor,
	})
	if err != nil {
		return err
	}

	s.log.DebugContext(ctx, "процесс заказа подтверждён",
		slog.String("order", payload.Number))

	return nil
}
