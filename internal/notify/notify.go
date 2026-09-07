// Package notify — уведомления пользователю о судьбе заказа.
//
// В MVP «отправка пуша» — это структурная запись в лог: реального канала
// доставки нет, а важно показать, что уведомления живут отдельным
// потребителем событий и не мешают оформлению заказа.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/emotionlessdev/avito-kitchen/internal/events"
	"github.com/emotionlessdev/avito-kitchen/internal/kafkax"
	"github.com/emotionlessdev/avito-kitchen/internal/outbox"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Notifier слушает события заказов и «шлёт» уведомления.
type Notifier struct {
	pool     *pgxpool.Pool
	consumer string
	log      *slog.Logger
}

// NewNotifier собирает потребителя уведомлений.
func NewNotifier(pool *pgxpool.Pool, log *slog.Logger) *Notifier {
	return &Notifier{pool: pool, consumer: kafkax.GroupNotifier, log: log}
}

// Handle обрабатывает одно событие заказа.
//
// Дедупликация нужна и здесь: пользователю не нужны два одинаковых пуша
// из-за повторной доставки.
func (n *Notifier) Handle(ctx context.Context, record kafkax.Record) error {
	tx, err := n.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начало транзакции уведомлений: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	fresh, err := outbox.MarkProcessed(ctx, tx, n.consumer, record.EventID)
	if err != nil {
		return err
	}

	if !fresh {
		return nil
	}

	if err := n.notify(ctx, record); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("коммит уведомлений: %w", err)
	}

	return nil
}

func (n *Notifier) notify(ctx context.Context, record kafkax.Record) error {
	switch record.Type {
	case events.TypeOrderCreated:
		var payload events.OrderCreated
		if err := json.Unmarshal(record.Value, &payload); err != nil {
			return fmt.Errorf("разбор события заказа: %w", err)
		}

		n.log.InfoContext(ctx, "уведомление пользователю",
			slog.String("user_id", payload.UserID.String()),
			slog.String("order_number", payload.Number),
			slog.String("text", "Заказ "+payload.Number+" принят платформой"))

		return nil
	case events.TypeOrderStatusChanged:
		var payload events.OrderStatusChanged
		if err := json.Unmarshal(record.Value, &payload); err != nil {
			return fmt.Errorf("разбор события статуса: %w", err)
		}

		n.log.InfoContext(ctx, "уведомление пользователю",
			slog.String("user_id", payload.UserID.String()),
			slog.String("order_number", payload.Number),
			slog.String("status", payload.ToStatus),
			slog.String("text", statusText(payload.ToStatus, payload.Number)))

		return nil
	default:
		// Незнакомый тип — не ошибка: топик общий, и новые события появятся
		// раньше, чем этот консьюмер научится их читать.
		n.log.DebugContext(ctx, "событие пропущено", slog.String("event_type", record.Type))

		return nil
	}
}

// statusText — текст уведомления. Здесь он захардкожен: в проде это шаблоны
// с локализацией, но в MVP важно показать, что уведомления привязаны
// к переходам статуса, а не к записи в базу.
func statusText(status, number string) string {
	switch status {
	case "accepted":
		return "Заведение приняло заказ " + number
	case "rejected":
		return "Заведение не смогло выполнить заказ " + number
	case "expired":
		return "Заведение не подтвердило заказ " + number + ", деньги не списаны"
	case "cooking":
		return "Заказ " + number + " готовится"
	case "ready":
		return "Заказ " + number + " готов"
	case "delivering":
		return "Курьер везёт заказ " + number
	case "completed":
		return "Заказ " + number + " доставлен"
	case "cancelled":
		return "Заказ " + number + " отменён"
	case "payment_failed":
		return "Оплата заказа " + number + " не прошла"
	default:
		return "Статус заказа " + number + ": " + status
	}
}
