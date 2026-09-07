// Package stats — витрина показателей заведения за день (сценарий M8).
//
// Это проекция, а не отчёт по операционным таблицам: консьюмер order-events
// накапливает счётчики в venue_daily_stats, а Merchant API читает готовую
// строку. Считать то же самое агрегатом по orders было бы проще, но заставило
// бы отчёт конкурировать за горячую таблицу заказов — ровно того разделения,
// ради которого в системе есть Kafka, тогда бы и не было (docs/DESIGN.md §5).
package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/emotionlessdev/avito-kitchen/internal/events"
	"github.com/emotionlessdev/avito-kitchen/internal/kafkax"
	"github.com/emotionlessdev/avito-kitchen/internal/outbox"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Projector строит витрину статистики из событий заказов.
type Projector struct {
	pool     *pgxpool.Pool
	consumer string
	log      *slog.Logger
}

// NewProjector собирает потребителя витрины статистики.
func NewProjector(pool *pgxpool.Pool, log *slog.Logger) *Projector {
	return &Projector{pool: pool, consumer: kafkax.GroupMerchantStats, log: log}
}

// Handle применяет одно событие.
//
// Дедупликация и запись счётчиков идут одной транзакцией: инкремент
// не идемпотентен сам по себе, и повторная доставка события без этого
// накрутила бы витрине лишний заказ.
func (p *Projector) Handle(ctx context.Context, record kafkax.Record) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начало транзакции витрины: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	fresh, err := outbox.MarkProcessed(ctx, tx, p.consumer, record.EventID)
	if err != nil {
		return err
	}

	if !fresh {
		return nil
	}

	if err := p.project(ctx, tx, record); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("коммит витрины: %w", err)
	}

	return nil
}

func (p *Projector) project(ctx context.Context, tx pgx.Tx, record kafkax.Record) error {
	switch record.Type {
	case events.TypeOrderCreated:
		var payload events.OrderCreated
		if err := json.Unmarshal(record.Value, &payload); err != nil {
			return fmt.Errorf("разбор события заказа: %w", err)
		}

		return p.apply(ctx, tx, payload.VenueID.String(), payload.CreatedAt, delta{total: 1})
	case events.TypeOrderStatusChanged:
		var payload events.OrderStatusChanged
		if err := json.Unmarshal(record.Value, &payload); err != nil {
			return fmt.Errorf("разбор события статуса: %w", err)
		}

		change := deltaFor(payload)
		if change.isEmpty() {
			return nil
		}

		return p.apply(ctx, tx, payload.VenueID.String(), payload.OrderCreatedAt, change)
	default:
		// Топик общий: чужие события — не ошибка.
		p.log.DebugContext(ctx, "событие пропущено", slog.String("event_type", record.Type))

		return nil
	}
}

// delta — на сколько событие двигает счётчики дня.
type delta struct {
	total         int
	completed     int
	rejected      int
	expired       int
	revenueMinor  int64
	acceptSeconds int64
	acceptCount   int
}

func (d delta) isEmpty() bool { return d == delta{} }

// deltaFor переводит переход статуса в приращения витрины.
func deltaFor(payload events.OrderStatusChanged) delta {
	switch payload.ToStatus {
	case "completed":
		return delta{completed: 1, revenueMinor: payload.TotalAmountMinor}
	case "rejected":
		return delta{rejected: 1}
	case "expired":
		return delta{expired: 1}
	case "accepted":
		if payload.PlacedAt == nil {
			return delta{}
		}

		seconds := int64(payload.ChangedAt.Sub(*payload.PlacedAt).Seconds())
		if seconds < 0 {
			seconds = 0
		}

		return delta{acceptSeconds: seconds, acceptCount: 1}
	default:
		return delta{}
	}
}

// apply прибавляет приращения к строке дня.
//
// День считается в часовом поясе заведения прямо в SQL: «сегодня» для
// владельца заведения — это его сегодня, а не UTC, и переводить время
// в приложении значило бы держать таймзону в двух местах.
func (p *Projector) apply(
	ctx context.Context, tx pgx.Tx, venueID string, occurredAt any, change delta,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO venue_daily_stats (
			venue_id, stat_date, orders_total, orders_completed, orders_rejected,
			orders_expired, revenue_minor, accept_seconds_total, accept_count
		)
		SELECT v.id, (($2::timestamptz) AT TIME ZONE v.timezone)::date,
		       $3, $4, $5, $6, $7, $8, $9
		FROM venues v
		WHERE v.id = $1
		ON CONFLICT (venue_id, stat_date) DO UPDATE SET
			orders_total         = venue_daily_stats.orders_total + EXCLUDED.orders_total,
			orders_completed     = venue_daily_stats.orders_completed + EXCLUDED.orders_completed,
			orders_rejected      = venue_daily_stats.orders_rejected + EXCLUDED.orders_rejected,
			orders_expired       = venue_daily_stats.orders_expired + EXCLUDED.orders_expired,
			revenue_minor        = venue_daily_stats.revenue_minor + EXCLUDED.revenue_minor,
			accept_seconds_total = venue_daily_stats.accept_seconds_total + EXCLUDED.accept_seconds_total,
			accept_count         = venue_daily_stats.accept_count + EXCLUDED.accept_count,
			updated_at           = now()`,
		venueID, occurredAt,
		change.total, change.completed, change.rejected, change.expired,
		change.revenueMinor, change.acceptSeconds, change.acceptCount)
	if err != nil {
		return fmt.Errorf("обновление витрины статистики: %w", err)
	}

	return nil
}
