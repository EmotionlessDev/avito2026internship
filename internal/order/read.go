package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// orderColumns перечислен один раз: порядок обязан совпадать со scanOrder.
const orderColumns = `
	o.id, o.number, o.user_id, o.venue_id, v.name, o.status,
	o.items_amount_minor, o.delivery_fee_minor, o.total_amount_minor,
	o.delivery_address, o.delivery_lat, o.delivery_lon, o.contact_phone,
	o.comment, o.eta_minutes, o.cancel_reason, o.workflow_id,
	o.created_at, o.placed_at, o.completed_at`

// Filter — параметры выдачи ленты заказов.
type Filter struct {
	Statuses []Status
	Limit    int
	Offset   int
}

// ByUser отдаёт заказ пользователя. Чужой заказ неотличим от несуществующего:
// подобрать идентификатор и узнать о существовании чужого заказа нельзя.
func (r *Repository) ByUser(ctx context.Context, userID, orderID uuid.UUID) (Order, error) {
	return r.one(ctx, `o.id = $1 AND o.user_id = $2`, orderID, userID)
}

// ByVenue отдаёт заказ заведения. Тот же принцип: чужой заказ — 404.
func (r *Repository) ByVenue(ctx context.Context, venueID, orderID uuid.UUID) (Order, error) {
	return r.one(ctx, `o.id = $1 AND o.venue_id = $2`, orderID, venueID)
}

// ListByUser — история заказов пользователя.
func (r *Repository) ListByUser(ctx context.Context, userID uuid.UUID, filter Filter) ([]Order, error) {
	return r.list(ctx, `o.user_id = $1`, userID, filter)
}

// ListByVenue — лента заказов заведения: экран повара и поллинг-фолбэк POS.
func (r *Repository) ListByVenue(ctx context.Context, venueID uuid.UUID, filter Filter) ([]Order, error) {
	return r.list(ctx, `o.venue_id = $1`, venueID, filter)
}

// ByIdempotencyKey ищет заказ, оформленный этим же ключом.
//
// Нужен до валидации корзины: после успешного оформления корзина пуста,
// и повтор запроса, начатый с проверок, увидел бы пустую корзину вместо
// уже созданного заказа.
func (r *Repository) ByIdempotencyKey(
	ctx context.Context, userID uuid.UUID, key string,
) (Order, bool, error) {
	found, err := r.one(ctx, `o.user_id = $1 AND o.idempotency_key = $2`, userID, key)
	if errors.Is(err, ErrNotFound) {
		return Order{}, false, nil
	}

	if err != nil {
		return Order{}, false, err
	}

	return found, true, nil
}

func (r *Repository) byID(ctx context.Context, orderID uuid.UUID) (Order, error) {
	return r.one(ctx, `o.id = $1`, orderID)
}

func (r *Repository) one(ctx context.Context, where string, args ...any) (Order, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT`+orderColumns+`
		FROM orders o
		JOIN venues v ON v.id = o.venue_id
		WHERE `+where, args...)
	if err != nil {
		return Order{}, fmt.Errorf("выборка заказа: %w", err)
	}

	orders, err := collectOrders(rows)
	if err != nil {
		return Order{}, err
	}

	if len(orders) == 0 {
		return Order{}, ErrNotFound
	}

	if err := r.attachDetails(ctx, orders); err != nil {
		return Order{}, err
	}

	return orders[0], nil
}

func (r *Repository) list(
	ctx context.Context, where string, owner uuid.UUID, filter Filter,
) ([]Order, error) {
	statuses := make([]string, 0, len(filter.Statuses))
	for _, status := range filter.Statuses {
		statuses = append(statuses, string(status))
	}

	rows, err := r.pool.Query(ctx, `
		SELECT`+orderColumns+`
		FROM orders o
		JOIN venues v ON v.id = o.venue_id
		WHERE `+where+`
		  AND (cardinality($2::text[]) = 0 OR o.status = ANY($2))
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT $3 OFFSET $4`, owner, statuses, filter.Limit, filter.Offset)
	if err != nil {
		return nil, fmt.Errorf("выборка заказов: %w", err)
	}

	orders, err := collectOrders(rows)
	if err != nil {
		return nil, err
	}

	if err := r.attachDetails(ctx, orders); err != nil {
		return nil, err
	}

	return orders, nil
}

func collectOrders(rows pgx.Rows) ([]Order, error) {
	defer rows.Close()

	var orders []Order

	for rows.Next() {
		var (
			current Order
			comment *string
		)

		if err := rows.Scan(
			&current.ID, &current.Number, &current.UserID, &current.VenueID, &current.VenueName,
			&current.Status, &current.ItemsAmountMinor, &current.DeliveryFeeMinor,
			&current.TotalAmountMinor, &current.DeliveryAddress, &current.DeliveryPoint.Lat,
			&current.DeliveryPoint.Lon, &current.ContactPhone, &comment, &current.ETAMinutes,
			&current.CancelReason, &current.WorkflowID, &current.CreatedAt,
			&current.PlacedAt, &current.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("чтение заказа: %w", err)
		}

		if comment != nil {
			current.Comment = *comment
		}

		orders = append(orders, current)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение заказов: %w", err)
	}

	return orders, nil
}

// attachDetails дочитывает состав и историю статусов одним запросом на всю
// страницу: иначе лента заказов заведения делала бы по два запроса на строку.
func (r *Repository) attachDetails(ctx context.Context, orders []Order) error {
	if len(orders) == 0 {
		return nil
	}

	index := make(map[uuid.UUID]int, len(orders))
	ids := make([]uuid.UUID, 0, len(orders))

	for i := range orders {
		index[orders[i].ID] = i
		ids = append(ids, orders[i].ID)
	}

	if err := r.attachItems(ctx, orders, index, ids); err != nil {
		return err
	}

	return r.attachTimeline(ctx, orders, index, ids)
}

func (r *Repository) attachItems(
	ctx context.Context, orders []Order, index map[uuid.UUID]int, ids []uuid.UUID,
) error {
	rows, err := r.pool.Query(ctx, `
		SELECT order_id, id, menu_item_id, external_item_id, name_snapshot,
		       price_minor_snapshot, qty, total_minor, options_snapshot
		FROM order_items
		WHERE order_id = ANY($1)
		ORDER BY order_id, id`, ids)
	if err != nil {
		return fmt.Errorf("состав заказов: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var (
			orderID uuid.UUID
			item    Item
			raw     []byte
		)

		if err := rows.Scan(
			&orderID, &item.ID, &item.MenuItemID, &item.ExternalItemID, &item.Name,
			&item.PriceMinor, &item.Qty, &item.TotalMinor, &raw,
		); err != nil {
			return fmt.Errorf("чтение позиции заказа: %w", err)
		}

		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &item.Options); err != nil {
				return fmt.Errorf("разбор модификаторов заказа: %w", err)
			}
		}

		position, known := index[orderID]
		if !known {
			continue
		}

		orders[position].Items = append(orders[position].Items, item)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("чтение состава заказов: %w", err)
	}

	return nil
}

func (r *Repository) attachTimeline(
	ctx context.Context, orders []Order, index map[uuid.UUID]int, ids []uuid.UUID,
) error {
	rows, err := r.pool.Query(ctx, `
		SELECT order_id, from_status, to_status, actor_type, reason, created_at
		FROM order_status_history
		WHERE order_id = ANY($1)
		ORDER BY order_id, created_at, id`, ids)
	if err != nil {
		return fmt.Errorf("история статусов: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var (
			orderID uuid.UUID
			change  StatusChange
		)

		if err := rows.Scan(
			&orderID, &change.FromStatus, &change.ToStatus,
			&change.ActorType, &change.Reason, &change.CreatedAt,
		); err != nil {
			return fmt.Errorf("чтение истории статусов: %w", err)
		}

		position, known := index[orderID]
		if !known {
			continue
		}

		orders[position].Timeline = append(orders[position].Timeline, change)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("чтение истории статусов: %w", err)
	}

	return nil
}
