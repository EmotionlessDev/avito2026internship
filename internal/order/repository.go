package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/events"
	"github.com/emotionlessdev/avito-kitchen/internal/outbox"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository — хранилище заказов.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository собирает репозиторий заказов.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Draft — заказ, готовый к записи. Суммы и состав уже посчитаны каскадом
// валидации: репозиторий ничего не пересчитывает, он только сохраняет.
type Draft struct {
	UserID           uuid.UUID
	VenueID          uuid.UUID
	VenueTimezone    string
	ItemsAmountMinor int64
	DeliveryFeeMinor int64
	TotalAmountMinor int64
	DeliveryAddress  string
	DeliveryPoint    Point
	ContactPhone     string
	Comment          string
	IdempotencyKey   string
	Fingerprint      string
	VenueName        string
	Items            []DraftItem
}

// DraftItem — позиция будущего заказа.
type DraftItem struct {
	MenuItemID uuid.UUID
	Name       string
	PriceMinor int64
	Qty        int
	TotalMinor int64
	OptionIDs  []uuid.UUID
}

// Create оформляет заказ одной транзакцией.
//
// В транзакцию входит всё, что обязано стать видимым вместе: сам заказ,
// снимок состава, первая запись истории и событие в outbox. Иначе появился
// бы момент, когда заказ уже есть, а факта о нём никто не получил
// (docs/DESIGN.md §5).
//
// Второе возвращаемое значение — признак «заказ создан сейчас». Повтор
// с тем же ключом идемпотентности возвращает тот же заказ и false.
func (r *Repository) Create(ctx context.Context, draft Draft) (Order, bool, error) {
	var (
		created bool
		orderID uuid.UUID
	)

	err := r.createOnce(ctx, draft, &created, &orderID)

	// Проверка ключа и вставка — это check-then-act: между ними успевает
	// вклиниться параллельный запрос с тем же ключом, и уникальный индекс
	// ловит его на INSERT. Перечитываем заказ, который успел создать сосед.
	if isDuplicateIdempotencyKey(err) {
		existing, found, lookupErr := r.ByIdempotencyKey(ctx, draft.UserID, draft.IdempotencyKey)
		if lookupErr != nil {
			return Order{}, false, lookupErr
		}

		if found {
			// Отпечаток сравнивается и здесь, а не только в сервисе: в гонке
			// оба запроса проходят его проверку до коммита соседа, и без
			// повторного сравнения запрос с другим адресом получил бы 200
			// и чужой заказ вместо 409.
			if existing.Fingerprint != draft.Fingerprint {
				return Order{}, false, ErrIdempotencyConflict
			}

			return existing, false, nil
		}
	}

	if err != nil {
		return Order{}, false, err
	}

	order, err := r.byID(ctx, orderID)
	if err != nil {
		return Order{}, false, err
	}

	return order, created, nil
}

// createOnce — сама транзакция оформления. Вынесена, чтобы гонку по ключу
// идемпотентности можно было обработать снаружи, не разворачивая транзакцию.
func (r *Repository) createOnce(
	ctx context.Context, draft Draft, created *bool, orderID *uuid.UUID,
) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		existing, found, err := findByIdempotencyKey(ctx, tx, draft.UserID, draft.IdempotencyKey)
		if err != nil {
			return err
		}

		if found {
			*orderID = existing

			return nil
		}

		var number string

		*orderID, number, err = insertOrder(ctx, tx, draft)
		if err != nil {
			return err
		}

		if err := insertItems(ctx, tx, *orderID, draft.Items); err != nil {
			return err
		}

		if err := insertStatusChange(ctx, tx, *orderID, nil, StatusCreated, ActorSystem, nil); err != nil {
			return err
		}

		if err := writeCreatedEvent(ctx, tx, draft, *orderID, number); err != nil {
			return err
		}

		*created = true

		return nil
	})
}

// idempotencyKeyConstraint — уникальный индекс, который защищает от второго
// заказа по тому же ключу. Имя проверяется явно: другой конфликт уникальности
// прятать под «это просто ретрай» нельзя.
const idempotencyKeyConstraint = "orders_user_id_idempotency_key_key"

func isDuplicateIdempotencyKey(err error) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) &&
		pgErr.Code == pgerrcode.UniqueViolation &&
		pgErr.ConstraintName == idempotencyKeyConstraint
}

func findByIdempotencyKey(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID, key string,
) (uuid.UUID, bool, error) {
	var id uuid.UUID

	err := tx.QueryRow(ctx, `
		SELECT id FROM orders WHERE user_id = $1 AND idempotency_key = $2`, userID, key).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}

	if err != nil {
		return uuid.Nil, false, fmt.Errorf("поиск заказа по ключу идемпотентности: %w", err)
	}

	return id, true, nil
}

func insertOrder(ctx context.Context, tx pgx.Tx, draft Draft) (uuid.UUID, string, error) {
	var sequence int64
	if err := tx.QueryRow(ctx, `SELECT nextval('order_number_seq')`).Scan(&sequence); err != nil {
		return uuid.Nil, "", fmt.Errorf("номер заказа: %w", err)
	}

	location, err := time.LoadLocation(draft.VenueTimezone)
	if err != nil {
		location = time.UTC
	}

	orderID := newID()
	number := FormatNumber(time.Now().In(location), sequence)

	_, err = tx.Exec(ctx, `
		INSERT INTO orders (id, number, user_id, venue_id, status,
		                    items_amount_minor, delivery_fee_minor, total_amount_minor,
		                    delivery_address, delivery_lat, delivery_lon, contact_phone,
		                    comment, idempotency_key, idempotency_fingerprint, workflow_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		orderID, number, draft.UserID, draft.VenueID, StatusCreated,
		draft.ItemsAmountMinor, draft.DeliveryFeeMinor, draft.TotalAmountMinor,
		draft.DeliveryAddress, draft.DeliveryPoint.Lat, draft.DeliveryPoint.Lon,
		draft.ContactPhone, draft.Comment, draft.IdempotencyKey, draft.Fingerprint,
		WorkflowIDFor(orderID))
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("создание заказа: %w", err)
	}

	return orderID, number, nil
}

// insertItems пишет снимок состава.
//
// Артикулы заведения подставляются подзапросом из меню: они нужны POS,
// а тянуть их через корзину значило бы протащить деталь интеграции
// через весь клиентский путь.
func insertItems(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, items []DraftItem) error {
	for _, item := range items {
		options, err := optionsSnapshot(ctx, tx, item.OptionIDs)
		if err != nil {
			return err
		}

		encoded, err := json.Marshal(options)
		if err != nil {
			return fmt.Errorf("сериализация модификаторов заказа: %w", err)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO order_items (id, order_id, menu_item_id, external_item_id, name_snapshot,
			                         price_minor_snapshot, qty, options_snapshot, total_minor)
			VALUES ($1, $2, $3, (SELECT external_id FROM menu_items WHERE id = $3), $4, $5, $6, $7, $8)`,
			newID(), orderID, item.MenuItemID, item.Name, item.PriceMinor, item.Qty, encoded, item.TotalMinor)
		if err != nil {
			return fmt.Errorf("сохранение позиции заказа: %w", err)
		}
	}

	return nil
}

func optionsSnapshot(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) ([]ItemOption, error) {
	options := make([]ItemOption, 0, len(ids))
	if len(ids) == 0 {
		return options, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT id, external_id, name, price_delta_minor
		FROM menu_options WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("модификаторы заказа: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var option ItemOption

		if err := rows.Scan(
			&option.OptionID, &option.ExternalID, &option.Name, &option.PriceDeltaMinor,
		); err != nil {
			return nil, fmt.Errorf("чтение модификатора заказа: %w", err)
		}

		options = append(options, option)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение модификаторов заказа: %w", err)
	}

	return options, nil
}

func insertStatusChange(
	ctx context.Context, tx pgx.Tx, orderID uuid.UUID,
	from *Status, to Status, actor Actor, reason *string,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO order_status_history (id, order_id, from_status, to_status, actor_type, reason)
		VALUES ($1, $2, $3, $4, $5, $6)`, newID(), orderID, from, to, actor, reason)
	if err != nil {
		return fmt.Errorf("запись истории статусов: %w", err)
	}

	return nil
}

// writeCreatedEvent кладёт факт оформления в outbox — в ту же транзакцию,
// что и сам заказ. Наблюдателям (уведомления, статистика) хватает сводки:
// полный состав всегда можно прочитать из Postgres.
func writeCreatedEvent(ctx context.Context, tx pgx.Tx, draft Draft, orderID uuid.UUID, number string) error {
	return outbox.Write(ctx, tx, outbox.Event{
		AggregateType: events.AggregateOrder,
		AggregateID:   orderID,
		Type:          events.TypeOrderCreated,
		Payload: events.OrderCreated{
			OrderID:          orderID,
			Number:           number,
			UserID:           draft.UserID,
			VenueID:          draft.VenueID,
			VenueName:        draft.VenueName,
			ItemsCount:       len(draft.Items),
			TotalAmountMinor: draft.TotalAmountMinor,
			CreatedAt:        time.Now().UTC(),
		},
	})
}

func (r *Repository) inTx(ctx context.Context, do func(tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начало транзакции: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	if err := do(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("коммит: %w", err)
	}

	return nil
}

func newID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}

	return id
}
