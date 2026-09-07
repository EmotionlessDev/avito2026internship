// Package events — общий словарь доменных событий платформы.
//
// Пакет намеренно пустой от логики: он описывает, что именно едет по Kafka.
// Продюсеры (каталог, заказы) и консьюмеры (индексатор поиска, уведомления)
// зависят от него, а не друг от друга.
package events

import (
	"time"

	"github.com/google/uuid"
)

// Типы агрегатов. Значение попадает в outbox.aggregate_type и в CHECK
// миграции 0004.
const (
	AggregateOrder    = "order"
	AggregateMenuItem = "menu_item"
	AggregateVenue    = "venue"
)

// Типы событий. Имя события — часть контракта топика, поэтому меняется
// только вместе с версией топика.
const (
	// TypeMenuItemChanged — позиция меню изменилась: цена, название,
	// доступность или состояние заведения.
	TypeMenuItemChanged = "menu_item.changed"
	// TypeMenuItemArchived — позиция исчезла из меню заведения.
	TypeMenuItemArchived = "menu_item.archived"
	// TypeOrderCreated — заказ оформлен и принят платформой.
	TypeOrderCreated = "order.created"
	// TypeOrderStatusChanged — заказ сменил статус.
	TypeOrderStatusChanged = "order.status_changed"
)

// MenuItemSnapshot — событие каталога.
//
// Это полный снимок позиции, а не дельта: топик catalog-events
// компактируемый, и после компакции по ключу остаётся только последнее
// сообщение — оно обязано быть самодостаточным (docs/DESIGN.md §5).
// Из такого топика витрину поиска можно пересобрать с нуля.
type MenuItemSnapshot struct {
	ItemID           uuid.UUID `json:"item_id"`
	VenueID          uuid.UUID `json:"venue_id"`
	ItemName         string    `json:"item_name"`
	Description      string    `json:"description"`
	VenueName        string    `json:"venue_name"`
	CategoryName     string    `json:"category_name"`
	PriceMinor       int64     `json:"price_minor"`
	IsAvailable      bool      `json:"is_available"`
	IsArchived       bool      `json:"is_archived"`
	VenueIsActive    bool      `json:"venue_is_active"`
	VenueIsAccepting bool      `json:"venue_is_accepting"`
	Lat              float64   `json:"lat"`
	Lon              float64   `json:"lon"`
	ChangedAt        time.Time `json:"changed_at"`
}

// OrderCreated — событие оформления заказа.
//
// Наблюдателям (уведомления, статистика) не нужен весь заказ: им хватает
// того, по чему они принимают решение. Полный состав лежит в Postgres.
type OrderCreated struct {
	OrderID          uuid.UUID `json:"order_id"`
	Number           string    `json:"number"`
	UserID           uuid.UUID `json:"user_id"`
	VenueID          uuid.UUID `json:"venue_id"`
	VenueName        string    `json:"venue_name"`
	ItemsCount       int       `json:"items_count"`
	TotalAmountMinor int64     `json:"total_amount_minor"`
	CreatedAt        time.Time `json:"created_at"`
}

// OrderStatusChanged — событие перехода статуса.
type OrderStatusChanged struct {
	OrderID    uuid.UUID `json:"order_id"`
	Number     string    `json:"number"`
	UserID     uuid.UUID `json:"user_id"`
	VenueID    uuid.UUID `json:"venue_id"`
	FromStatus string    `json:"from_status,omitempty"`
	ToStatus   string    `json:"to_status"`
	ActorType  string    `json:"actor_type"`
	Reason     string    `json:"reason,omitempty"`
	ChangedAt  time.Time `json:"changed_at"`
}
