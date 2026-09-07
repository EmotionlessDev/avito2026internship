package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/events"
	"github.com/emotionlessdev/avito-kitchen/internal/outbox"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Transition — запрошенный переход статуса.
type Transition struct {
	OrderID uuid.UUID
	To      Status
	Actor   Actor
	Reason  string
	// ETAMinutes проставляется вместе с подтверждением заказа.
	ETAMinutes *int
}

// ApplyStatus переводит заказ в новый статус.
//
// Здесь сходятся три вещи, которые обязаны стать видимыми вместе: сам статус,
// запись в историю и событие для наблюдателей. Отдельно — проверка машины
// состояний: она делается внутри транзакции по текущему статусу из базы,
// поэтому два одновременных перехода не разъедутся.
//
// Метод идемпотентен по смыслу: повторный перевод в тот же статус ничего
// не меняет и не считается ошибкой — активность Temporal может выполниться
// дважды, и падать на этом нельзя.
func (r *Repository) ApplyStatus(ctx context.Context, transition Transition) (Order, error) {
	var updated Order

	err := r.inTx(ctx, func(tx pgx.Tx) error {
		locked, err := lockOrder(ctx, tx, transition.OrderID)
		if err != nil {
			return err
		}

		if locked.Status == transition.To {
			// Уже в этом статусе: активность выполнилась повторно.
			return nil
		}

		if err := CanTransition(locked.Status, transition.To, transition.Actor); err != nil {
			return err
		}

		if err := writeStatus(ctx, tx, transition, locked.Status); err != nil {
			return err
		}

		changedAt := time.Now().UTC()

		// placed_at на самом переходе в placed ещё не записан в прочитанной
		// строке, поэтому берём момент перехода: это и есть placed_at.
		placedAt := locked.PlacedAt
		if transition.To == StatusPlaced {
			placedAt = &changedAt
		}

		return outbox.Write(ctx, tx, outbox.Event{
			AggregateType: events.AggregateOrder,
			AggregateID:   transition.OrderID,
			Type:          events.TypeOrderStatusChanged,
			Payload: events.OrderStatusChanged{
				OrderID:          transition.OrderID,
				Number:           locked.Number,
				UserID:           locked.UserID,
				VenueID:          locked.VenueID,
				FromStatus:       string(locked.Status),
				ToStatus:         string(transition.To),
				ActorType:        string(transition.Actor),
				Reason:           transition.Reason,
				ChangedAt:        changedAt,
				TotalAmountMinor: locked.TotalAmountMinor,
				OrderCreatedAt:   locked.CreatedAt,
				PlacedAt:         placedAt,
			},
		})
	})
	if err != nil {
		return Order{}, err
	}

	updated, err = r.byID(ctx, transition.OrderID)
	if err != nil {
		return Order{}, err
	}

	return updated, nil
}

// lockedOrder — то, что нужно знать о заказе, чтобы применить переход
// и собрать событие.
type lockedOrder struct {
	Status           Status
	Number           string
	UserID           uuid.UUID
	VenueID          uuid.UUID
	TotalAmountMinor int64
	CreatedAt        time.Time
	PlacedAt         *time.Time
}

// lockOrder читает заказ под блокировку строки: пока идёт проверка перехода,
// соседний вызов не должен подменить статус.
func lockOrder(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (lockedOrder, error) {
	var locked lockedOrder

	err := tx.QueryRow(ctx, `
		SELECT status, number, user_id, venue_id, total_amount_minor, created_at, placed_at
		FROM orders WHERE id = $1 FOR UPDATE`, orderID).
		Scan(&locked.Status, &locked.Number, &locked.UserID, &locked.VenueID,
			&locked.TotalAmountMinor, &locked.CreatedAt, &locked.PlacedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return lockedOrder{}, ErrNotFound
	}

	if err != nil {
		return lockedOrder{}, fmt.Errorf("чтение заказа под блокировкой: %w", err)
	}

	return locked, nil
}

func writeStatus(ctx context.Context, tx pgx.Tx, transition Transition, from Status) error {
	// placed_at и completed_at заполняются здесь же: доставать их из истории
	// каждый раз дороже, а витрине статистики они нужны постоянно.
	_, err := tx.Exec(ctx, `
		UPDATE orders SET
			status = $2,
			eta_minutes = COALESCE($3, eta_minutes),
			cancel_reason = CASE WHEN $2 IN ('cancelled', 'rejected', 'expired')
			                     THEN NULLIF($4, '') ELSE cancel_reason END,
			placed_at = CASE WHEN $2 = 'placed' THEN now() ELSE placed_at END,
			completed_at = CASE WHEN $2 = 'completed' THEN now() ELSE completed_at END,
			updated_at = now()
		WHERE id = $1`,
		transition.OrderID, transition.To, transition.ETAMinutes, transition.Reason)
	if err != nil {
		return fmt.Errorf("обновление статуса заказа: %w", err)
	}

	var reason *string
	if transition.Reason != "" {
		reason = &transition.Reason
	}

	return insertStatusChange(ctx, tx, transition.OrderID, &from, transition.To, transition.Actor, reason)
}

// SavePayment сохраняет состояние платежа по заказу.
//
// Один заказ — один платёж, поэтому запись идёт upsert'ом: повторный вызов
// активности обновит статус, а не заведёт второй холд.
func (r *Repository) SavePayment(
	ctx context.Context, orderID uuid.UUID, status string, amountMinor int64, providerRef string,
) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO payments (id, order_id, status, amount_minor, provider_ref)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (order_id) DO UPDATE
		SET status = EXCLUDED.status,
		    provider_ref = EXCLUDED.provider_ref,
		    updated_at = now()`,
		newID(), orderID, status, amountMinor, providerRef)
	if err != nil {
		return fmt.Errorf("сохранение платежа: %w", err)
	}

	return nil
}

// PaymentRef отдаёт ссылку на холд по заказу: её нужно знать компенсации.
func (r *Repository) PaymentRef(ctx context.Context, orderID uuid.UUID) (string, error) {
	var ref string

	err := r.pool.QueryRow(ctx,
		`SELECT provider_ref FROM payments WHERE order_id = $1`, orderID).Scan(&ref)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("чтение платежа: %w", err)
	}

	return ref, nil
}

// Webhook — куда доставлять заказ заведению.
type Webhook struct {
	URL    string
	Secret string
}

// VenueWebhook отдаёт зарегистрированный вебхук заведения.
// Пустой адрес означает, что заведение вебхук не регистрировало и заберёт
// заказ поллингом (сценарий M6).
func (r *Repository) VenueWebhook(ctx context.Context, venueID uuid.UUID) (Webhook, error) {
	var webhook Webhook

	err := r.pool.QueryRow(ctx, `
		SELECT url, secret FROM venue_webhooks
		WHERE venue_id = $1 AND is_active`, venueID).Scan(&webhook.URL, &webhook.Secret)

	if errors.Is(err, pgx.ErrNoRows) {
		return Webhook{}, nil
	}

	if err != nil {
		return Webhook{}, fmt.Errorf("чтение вебхука заведения: %w", err)
	}

	return webhook, nil
}

// StatusOf отдаёт текущий статус заказа — нужен активностям, чтобы не писать
// то, что уже записано.
func (r *Repository) StatusOf(ctx context.Context, orderID uuid.UUID) (Status, error) {
	var status Status

	err := r.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, orderID).Scan(&status)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}

	if err != nil {
		return "", fmt.Errorf("чтение статуса заказа: %w", err)
	}

	return status, nil
}
