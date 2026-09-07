package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // драйвер SQLite на чистом Go: бинарь остаётся статическим
)

// Статусы заказа внутри заведения. Они свои: у платформы своя машина
// состояний, у кухни — своя, и совпадать они не обязаны.
const (
	statusNew       = "new"
	statusAccepted  = "accepted"
	statusReady     = "ready"
	statusDelivered = "delivered"
	statusRejected  = "rejected"
	statusCancelled = "cancelled"
)

// Storage — хранилище заведения.
//
// SQLite, а не таблица в базе платформы: заведение — отдельная система,
// и его заказы живут у него. Это же делает демо честным — видно, что
// интеграция идёт через API, а не через общую базу.
type Storage struct {
	db *sql.DB
}

// OpenStorage открывает файл базы и создаёт схему.
func OpenStorage(ctx context.Context, path string) (*Storage, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("открытие базы POS: %w", err)
	}

	// Один писатель: SQLite не любит параллельную запись, а нагрузки здесь нет.
	db.SetMaxOpenConns(1)

	storage := &Storage{db: db}
	if err := storage.migrate(ctx); err != nil {
		return nil, err
	}

	return storage, nil
}

// Close закрывает базу.
func (s *Storage) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("закрытие базы POS: %w", err)
	}

	return nil
}

func (s *Storage) migrate(ctx context.Context) error {
	const schema = `
		CREATE TABLE IF NOT EXISTS orders (
			id           TEXT PRIMARY KEY,
			number       TEXT NOT NULL,
			status       TEXT NOT NULL,
			payload      TEXT NOT NULL,
			received_at  TIMESTAMP NOT NULL,
			act_after    TIMESTAMP NOT NULL
		);

		CREATE TABLE IF NOT EXISTS processed_events (
			event_id     TEXT PRIMARY KEY,
			processed_at TIMESTAMP NOT NULL
		);

		CREATE TABLE IF NOT EXISTS menu_items (
			external_id  TEXT PRIMARY KEY,
			category     TEXT NOT NULL,
			name         TEXT NOT NULL,
			description  TEXT NOT NULL DEFAULT '',
			price_minor  INTEGER NOT NULL,
			position     INTEGER NOT NULL DEFAULT 0,
			is_available INTEGER NOT NULL DEFAULT 1
		);`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("схема базы POS: %w", err)
	}

	return nil
}

// Order — заказ глазами кухни.
type Order struct {
	ID         string
	Number     string
	Status     string
	Payload    string
	ReceivedAt time.Time
	// ActAfter — момент, раньше которого автопилот заказ не трогает:
	// повар не подтверждает и не готовит мгновенно.
	ActAfter time.Time
}

// SaveOrder кладёт новый заказ. Повторная доставка того же заказа ничего
// не меняет: у платформы at-least-once, и второй вызов — норма.
func (s *Storage) SaveOrder(ctx context.Context, order Order) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO orders (id, number, status, payload, received_at, act_after)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		order.ID, order.Number, order.Status, order.Payload, order.ReceivedAt, order.ActAfter)
	if err != nil {
		return false, fmt.Errorf("сохранение заказа: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("сохранение заказа: %w", err)
	}

	return affected == 1, nil
}

// SetStatus меняет статус заказа и назначает момент следующего действия.
func (s *Storage) SetStatus(ctx context.Context, orderID, status string, actAfter time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE orders SET status = ?, act_after = ? WHERE id = ?`, status, actAfter, orderID)
	if err != nil {
		return fmt.Errorf("смена статуса заказа: %w", err)
	}

	return nil
}

// OrdersDue отдаёт заказы, до которых у кухни дошли руки: статус подходящий
// и назначенный момент наступил.
func (s *Storage) OrdersDue(ctx context.Context, status string, now time.Time) ([]Order, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, number, status, payload, received_at, act_after
		FROM orders WHERE status = ? AND act_after <= ?
		ORDER BY received_at`, status, now)
	if err != nil {
		return nil, fmt.Errorf("выборка заказов: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var orders []Order

	for rows.Next() {
		var order Order

		if err := rows.Scan(&order.ID, &order.Number, &order.Status, &order.Payload,
			&order.ReceivedAt, &order.ActAfter); err != nil {
			return nil, fmt.Errorf("чтение заказа: %w", err)
		}

		orders = append(orders, order)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение заказов: %w", err)
	}

	return orders, nil
}

// ListOrders отдаёт последние заказы — это экран повара.
func (s *Storage) ListOrders(ctx context.Context, limit int) ([]Order, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, number, status, payload, received_at, act_after
		FROM orders ORDER BY received_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("список заказов: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var orders []Order

	for rows.Next() {
		var order Order

		if err := rows.Scan(&order.ID, &order.Number, &order.Status, &order.Payload,
			&order.ReceivedAt, &order.ActAfter); err != nil {
			return nil, fmt.Errorf("чтение заказа: %w", err)
		}

		orders = append(orders, order)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение заказов: %w", err)
	}

	return orders, nil
}

// OrderByID отдаёт один заказ.
func (s *Storage) OrderByID(ctx context.Context, orderID string) (Order, error) {
	var order Order

	err := s.db.QueryRowContext(ctx, `
		SELECT id, number, status, payload, received_at, act_after
		FROM orders WHERE id = ?`, orderID).
		Scan(&order.ID, &order.Number, &order.Status, &order.Payload,
			&order.ReceivedAt, &order.ActAfter)

	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrOrderNotFound
	}

	if err != nil {
		return Order{}, fmt.Errorf("чтение заказа: %w", err)
	}

	return order, nil
}

// ErrOrderNotFound — заказа нет в системе заведения.
var ErrOrderNotFound = errors.New("заказ не найден")

// MarkEventProcessed отмечает событие обработанным и говорит, впервые ли
// оно пришло. Дедупликация обязательна: платформа доставляет at-least-once.
func (s *Storage) MarkEventProcessed(ctx context.Context, eventID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO processed_events (event_id, processed_at) VALUES (?, ?)
		ON CONFLICT (event_id) DO NOTHING`, eventID, time.Now().UTC())
	if err != nil {
		return false, fmt.Errorf("отметка события: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("отметка события: %w", err)
	}

	return affected == 1, nil
}

// MenuItem — позиция меню заведения.
type MenuItem struct {
	ExternalID  string `json:"external_id"`
	Category    string `json:"category"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	Position    int    `json:"position"`
	IsAvailable bool   `json:"is_available"`
}

// UpsertMenu записывает меню заведения.
func (s *Storage) UpsertMenu(ctx context.Context, items []MenuItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("транзакция меню: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	for _, item := range items {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO menu_items (external_id, category, name, description, price_minor, position, is_available)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (external_id) DO UPDATE SET
				category = excluded.category,
				name = excluded.name,
				description = excluded.description,
				position = excluded.position`,
			item.ExternalID, item.Category, item.Name, item.Description,
			item.PriceMinor, item.Position, boolToInt(item.IsAvailable))
		if err != nil {
			return fmt.Errorf("запись позиции меню: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("коммит меню: %w", err)
	}

	return nil
}

// Menu отдаёт меню заведения.
func (s *Storage) Menu(ctx context.Context) ([]MenuItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT external_id, category, name, description, price_minor, position, is_available
		FROM menu_items ORDER BY category, position, external_id`)
	if err != nil {
		return nil, fmt.Errorf("чтение меню: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var items []MenuItem

	for rows.Next() {
		var (
			item      MenuItem
			available int
		)

		if err := rows.Scan(&item.ExternalID, &item.Category, &item.Name, &item.Description,
			&item.PriceMinor, &item.Position, &available); err != nil {
			return nil, fmt.Errorf("чтение позиции меню: %w", err)
		}

		item.IsAvailable = available == 1
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение меню: %w", err)
	}

	return items, nil
}

// SetItemAvailability снимает позицию с продажи или возвращает её.
func (s *Storage) SetItemAvailability(ctx context.Context, externalID string, available bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE menu_items SET is_available = ? WHERE external_id = ?`,
		boolToInt(available), externalID)
	if err != nil {
		return fmt.Errorf("смена доступности позиции: %w", err)
	}

	return nil
}

// SetItemPrice меняет цену позиции — сценарий «цена выросла» (U7).
func (s *Storage) SetItemPrice(ctx context.Context, externalID string, priceMinor int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE menu_items SET price_minor = ? WHERE external_id = ?`, priceMinor, externalID)
	if err != nil {
		return fmt.Errorf("смена цены позиции: %w", err)
	}

	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}

	return 0
}
