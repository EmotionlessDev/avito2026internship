package cart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository — хранилище корзины.
//
// Корзина живёт в Postgres, а не в памяти или Redis: она переживает
// перезапуск сервиса и открывается на другом устройстве. Для MVP это ещё
// и бесплатно — база уже есть.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository собирает репозиторий корзины.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// UserExists проверяет, что пользователь из заголовка известен платформе.
//
// В MVP авторизации нет, X-User-Id приходит как есть (docs/DESIGN.md §11),
// поэтому единственная защита от опечатки и подстановки — существование
// пользователя в базе.
func (r *Repository) UserExists(ctx context.Context, userID uuid.UUID) (bool, error) {
	var exists bool

	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("проверка пользователя: %w", err)
	}

	return exists, nil
}

// Get отдаёт корзину пользователя с пересчитанными ценами.
//
// Пустая корзина — это корзина без строк, а не её отсутствие: клиенту
// удобнее всегда получать один и тот же объект.
func (r *Repository) Get(ctx context.Context, userID uuid.UUID) (Cart, error) {
	cart, err := r.header(ctx, userID)
	if err != nil {
		return Cart{}, err
	}

	lines, optionIDs, err := r.lines(ctx, cart.ID)
	if err != nil {
		return Cart{}, err
	}

	options, err := r.options(ctx, optionIDs)
	if err != nil {
		return Cart{}, err
	}

	for i := range lines {
		lines[i].Options = collectOptions(lines[i].Options, options)
		lines[i].UnitPriceMinor += optionsTotal(lines[i].Options)
	}

	cart.Lines = lines

	return cart, nil
}

// header читает саму корзину, создавая её при первом обращении: отдельной
// ручки «создать корзину» в контракте нет и быть не должно.
func (r *Repository) header(ctx context.Context, userID uuid.UUID) (Cart, error) {
	var (
		cart      Cart
		venueName *string
	)

	err := r.pool.QueryRow(ctx, `
		WITH created AS (
			INSERT INTO carts (id, user_id) VALUES ($1, $2)
			ON CONFLICT (user_id) DO NOTHING
			RETURNING id, user_id, venue_id, updated_at
		)
		SELECT c.id, c.user_id, c.venue_id, c.updated_at, v.name
		FROM (
			SELECT * FROM created
			UNION ALL
			SELECT id, user_id, venue_id, updated_at FROM carts WHERE user_id = $2
		) c
		LEFT JOIN venues v ON v.id = c.venue_id
		LIMIT 1`, uuid.New(), userID).
		Scan(&cart.ID, &cart.UserID, &cart.VenueID, &cart.UpdatedAt, &venueName)
	if err != nil {
		return Cart{}, fmt.Errorf("чтение корзины: %w", err)
	}

	if venueName != nil {
		cart.VenueName = *venueName
	}

	return cart, nil
}

// lines читает строки корзины вместе с актуальной ценой и доступностью
// позиции: обе меняются заведением, пока корзина лежит.
func (r *Repository) lines(ctx context.Context, cartID uuid.UUID) ([]Line, []uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT ci.id, ci.menu_item_id, i.name, ci.qty, ci.added_price_minor,
		       i.price_minor, (i.is_available AND NOT i.is_archived), ci.options
		FROM cart_items ci
		JOIN menu_items i ON i.id = ci.menu_item_id
		WHERE ci.cart_id = $1
		ORDER BY ci.created_at, ci.id`, cartID)
	if err != nil {
		return nil, nil, fmt.Errorf("чтение позиций корзины: %w", err)
	}

	defer rows.Close()

	var (
		lines     []Line
		optionIDs []uuid.UUID
	)

	for rows.Next() {
		var (
			line    Line
			rawJSON []byte
		)

		if err := rows.Scan(
			&line.ID, &line.MenuItemID, &line.Name, &line.Qty, &line.AddedUnitPriceMinor,
			&line.UnitPriceMinor, &line.IsAvailable, &rawJSON,
		); err != nil {
			return nil, nil, fmt.Errorf("чтение позиции корзины: %w", err)
		}

		selected, err := decodeOptionIDs(rawJSON)
		if err != nil {
			return nil, nil, err
		}

		for _, id := range selected {
			line.Options = append(line.Options, LineOption{OptionID: id})
			optionIDs = append(optionIDs, id)
		}

		lines = append(lines, line)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("чтение позиций корзины: %w", err)
	}

	return lines, optionIDs, nil
}

// options дочитывает выбранные модификаторы: их цена и доступность живут
// в меню и меняются заведением.
func (r *Repository) options(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]LineOption, error) {
	options := make(map[uuid.UUID]LineOption, len(ids))
	if len(ids) == 0 {
		return options, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT o.id, o.group_id, o.name, o.price_delta_minor, o.is_available
		FROM menu_options o
		WHERE o.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("чтение модификаторов корзины: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var option LineOption

		if err := rows.Scan(
			&option.OptionID, &option.GroupID, &option.Name,
			&option.PriceDeltaMinor, &option.IsAvailable,
		); err != nil {
			return nil, fmt.Errorf("чтение модификатора: %w", err)
		}

		options[option.OptionID] = option
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение модификаторов: %w", err)
	}

	return options, nil
}

// collectOptions подставляет к выбранным идентификаторам их текущее состояние.
// Опция, исчезнувшая из меню, остаётся в корзине недоступной: пользователю
// нужно объяснить, почему блюдо больше нельзя заказать в таком виде.
func collectOptions(selected []LineOption, known map[uuid.UUID]LineOption) []LineOption {
	filled := make([]LineOption, 0, len(selected))

	for _, option := range selected {
		current, found := known[option.OptionID]
		if !found {
			filled = append(filled, LineOption{
				OptionID:    option.OptionID,
				Name:        "Недоступный модификатор",
				IsAvailable: false,
			})

			continue
		}

		filled = append(filled, current)
	}

	return filled
}

func optionsTotal(options []LineOption) int64 {
	var total int64

	for i := range options {
		total += options[i].PriceDeltaMinor
	}

	return total
}

func decodeOptionIDs(raw []byte) ([]uuid.UUID, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var ids []uuid.UUID
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("разбор модификаторов корзины: %w", err)
	}

	return ids, nil
}

// MenuItem — позиция меню в том виде, в каком её нужно знать корзине:
// с заведением, ценой и группами модификаторов для проверки выбора.
type MenuItem struct {
	ID          uuid.UUID
	VenueID     uuid.UUID
	Name        string
	PriceMinor  int64
	IsAvailable bool
	Groups      []OptionGroup
}

// OptionGroup — группа модификаторов позиции с её ограничениями.
type OptionGroup struct {
	ID         uuid.UUID
	Name       string
	MinChoices int
	MaxChoices int
	Options    map[uuid.UUID]int64
}

// MenuItemForAdd читает позицию меню вместе с модификаторами.
// Архивные и снятые с продажи позиции в корзину не кладутся вовсе:
// добавить то, чего нет, нельзя даже «на будущее».
func (r *Repository) MenuItemForAdd(ctx context.Context, itemID uuid.UUID) (MenuItem, error) {
	var item MenuItem

	err := r.pool.QueryRow(ctx, `
		SELECT i.id, i.venue_id, i.name, i.price_minor, i.is_available
		FROM menu_items i
		JOIN venues v ON v.id = i.venue_id
		WHERE i.id = $1 AND NOT i.is_archived AND v.status = 'active'`, itemID).
		Scan(&item.ID, &item.VenueID, &item.Name, &item.PriceMinor, &item.IsAvailable)

	if errors.Is(err, pgx.ErrNoRows) {
		return MenuItem{}, ErrMenuItemNotFound
	}

	if err != nil {
		return MenuItem{}, fmt.Errorf("чтение позиции меню: %w", err)
	}

	groups, err := r.itemGroups(ctx, itemID)
	if err != nil {
		return MenuItem{}, err
	}

	item.Groups = groups

	return item, nil
}

func (r *Repository) itemGroups(ctx context.Context, itemID uuid.UUID) ([]OptionGroup, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT g.id, g.name, g.min_choices, g.max_choices,
		       o.id, o.price_delta_minor
		FROM menu_option_groups g
		LEFT JOIN menu_options o ON o.group_id = g.id AND o.is_available
		WHERE g.item_id = $1
		ORDER BY g.position, g.id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("чтение групп модификаторов: %w", err)
	}

	defer rows.Close()

	var groups []OptionGroup

	index := make(map[uuid.UUID]int)

	for rows.Next() {
		var (
			group    OptionGroup
			optionID *uuid.UUID
			delta    *int64
		)

		if err := rows.Scan(
			&group.ID, &group.Name, &group.MinChoices, &group.MaxChoices, &optionID, &delta,
		); err != nil {
			return nil, fmt.Errorf("чтение группы модификаторов: %w", err)
		}

		position, found := index[group.ID]
		if !found {
			group.Options = map[uuid.UUID]int64{}
			groups = append(groups, group)
			position = len(groups) - 1
			index[group.ID] = position
		}

		if optionID != nil && delta != nil {
			groups[position].Options[*optionID] = *delta
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение групп модификаторов: %w", err)
	}

	return groups, nil
}

// AddLine кладёт позицию в корзину.
//
// Одинаковая позиция с тем же набором модификаторов не плодит строки,
// а увеличивает количество: две строки «Хачапури без добавок» в корзине —
// это ошибка интерфейса, а не выбор пользователя.
func (r *Repository) AddLine(
	ctx context.Context, userID uuid.UUID, item MenuItem, optionIDs []uuid.UUID, qty int, unitPrice int64,
) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		cartID, venueID, err := lockCart(ctx, tx, userID)
		if err != nil {
			return err
		}

		if venueID != nil && *venueID != item.VenueID {
			return ErrMixedVenues
		}

		encoded, err := json.Marshal(normalizeIDs(optionIDs))
		if err != nil {
			return fmt.Errorf("сохранение модификаторов: %w", err)
		}

		if lineErr := upsertLine(ctx, tx, cartID, item.ID, encoded, qty, unitPrice); lineErr != nil {
			return lineErr
		}

		_, err = tx.Exec(ctx,
			`UPDATE carts SET venue_id = $2, updated_at = now() WHERE id = $1`, cartID, item.VenueID)
		if err != nil {
			return fmt.Errorf("привязка корзины к заведению: %w", err)
		}

		return nil
	})
}

// lockCart берёт корзину пользователя под блокировку строки: два запроса
// «добавить позицию» подряд не должны разъехаться по разным заведениям.
func lockCart(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID,
) (cartID uuid.UUID, venueID *uuid.UUID, err error) {
	err = tx.QueryRow(ctx, `
		INSERT INTO carts (id, user_id) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET updated_at = now()
		RETURNING id, venue_id`, uuid.New(), userID).Scan(&cartID, &venueID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("блокировка корзины: %w", err)
	}

	return cartID, venueID, nil
}

func upsertLine(
	ctx context.Context, tx pgx.Tx, cartID, itemID uuid.UUID, options []byte, qty int, unitPrice int64,
) error {
	var lineID uuid.UUID

	err := tx.QueryRow(ctx, `
		SELECT id FROM cart_items
		WHERE cart_id = $1 AND menu_item_id = $2 AND options = $3::jsonb`,
		cartID, itemID, options).Scan(&lineID)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = tx.Exec(ctx, `
			INSERT INTO cart_items (id, cart_id, menu_item_id, qty, options, added_price_minor)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6)`,
			uuid.New(), cartID, itemID, qty, options, unitPrice)
		if err != nil {
			return fmt.Errorf("добавление позиции в корзину: %w", err)
		}

		return nil
	case err != nil:
		return fmt.Errorf("поиск позиции в корзине: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE cart_items SET qty = qty + $2, updated_at = now() WHERE id = $1`,
		lineID, qty); err != nil {
		return fmt.Errorf("увеличение количества: %w", err)
	}

	return nil
}

// UpdateQty меняет количество строки корзины.
func (r *Repository) UpdateQty(ctx context.Context, userID, lineID uuid.UUID, qty int) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cart_items ci SET qty = $3, updated_at = now()
		FROM carts c
		WHERE ci.cart_id = c.id AND c.user_id = $1 AND ci.id = $2`, userID, lineID, qty)
	if err != nil {
		return fmt.Errorf("изменение количества: %w", err)
	}

	// Строка чужой корзины неотличима от несуществующей: подобрать чужой
	// идентификатор и узнать о его существовании нельзя.
	if tag.RowsAffected() == 0 {
		return ErrCartItemNotFound
	}

	return nil
}

// DeleteLine убирает строку из корзины и отвязывает корзину от заведения,
// если строк не осталось: пустая корзина не должна мешать заказать в другом
// заведении.
func (r *Repository) DeleteLine(ctx context.Context, userID, lineID uuid.UUID) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM cart_items ci
			USING carts c
			WHERE ci.cart_id = c.id AND c.user_id = $1 AND ci.id = $2`, userID, lineID)
		if err != nil {
			return fmt.Errorf("удаление позиции: %w", err)
		}

		if tag.RowsAffected() == 0 {
			return ErrCartItemNotFound
		}

		return detachEmptyCart(ctx, tx, userID)
	})
}

// Clear очищает корзину целиком — это ответ пользователя на вопрос
// «в корзине блюда другого заведения, очистить?» (сценарий U8).
func (r *Repository) Clear(ctx context.Context, userID uuid.UUID) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			DELETE FROM cart_items ci USING carts c
			WHERE ci.cart_id = c.id AND c.user_id = $1`, userID)
		if err != nil {
			return fmt.Errorf("очистка корзины: %w", err)
		}

		return detachEmptyCart(ctx, tx, userID)
	})
}

func detachEmptyCart(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE carts c SET venue_id = NULL, updated_at = now()
		WHERE c.user_id = $1
		  AND NOT EXISTS (SELECT 1 FROM cart_items ci WHERE ci.cart_id = c.id)`, userID)
	if err != nil {
		return fmt.Errorf("отвязка пустой корзины: %w", err)
	}

	return nil
}

// normalizeIDs приводит набор модификаторов к каноническому виду: порядок
// выбора не должен превращать одинаковые строки в разные.
func normalizeIDs(ids []uuid.UUID) []uuid.UUID {
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b uuid.UUID) int {
		switch {
		case a.String() < b.String():
			return -1
		case a.String() > b.String():
			return 1
		default:
			return 0
		}
	})

	if sorted == nil {
		return []uuid.UUID{}
	}

	return sorted
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
