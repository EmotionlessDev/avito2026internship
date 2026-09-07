// Package order — заказы: домен, машина состояний и оформление.
//
// Заказ здесь — это строка в Postgres и снимок состава на момент оформления.
// Процессом заказа (таймеры, сигналы, компенсации) владеет Temporal, а
// orders.status — проекция состояния workflow, которую пишут activities
// и читает HTTP API (главный ADR, docs/DESIGN.md §5).
package order

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Status — статус заказа. Значения совпадают с CHECK в миграции 0003_orders
// и со схемами в api/*.openapi.yaml: это один общий словарь на всю систему.
type Status string

// Полный перечень статусов.
const (
	StatusCreated       Status = "created"
	StatusPaymentFailed Status = "payment_failed"
	StatusPlaced        Status = "placed"
	StatusAccepted      Status = "accepted"
	StatusCooking       Status = "cooking"
	StatusReady         Status = "ready"
	StatusDelivering    Status = "delivering"
	StatusCompleted     Status = "completed"
	StatusRejected      Status = "rejected"
	StatusExpired       Status = "expired"
	StatusCancelled     Status = "cancelled"
)

// Actor — кто выполняет переход. Права актора — часть машины состояний:
// попытка заведения отменить заказ пользователя должна быть ошибкой,
// а не тихим переходом.
type Actor string

// Возможные акторы.
const (
	ActorUser   Actor = "user"
	ActorVenue  Actor = "venue"
	ActorSystem Actor = "system"
)

// Ошибки домена.
var (
	// ErrNotFound — заказа нет или он принадлежит другому пользователю
	// либо другому заведению.
	ErrNotFound = errors.New("заказ не найден")
	// ErrCartNotValid — корзину нельзя оформить: каскад нашёл блокирующие
	// проблемы.
	ErrCartNotValid = errors.New("корзину нельзя оформить")
	// ErrIdempotencyConflict — тот же ключ идемпотентности пришёл с другим
	// содержимым запроса.
	ErrIdempotencyConflict = errors.New("ключ идемпотентности уже использован")
)

// ErrNotCancellable — заказ уже нельзя отменить: машина состояний
// не разрешает переход (сценарий U11).
type ErrNotCancellable struct {
	Status Status
}

func (e ErrNotCancellable) Error() string {
	return "заказ в статусе " + string(e.Status) + " отменить нельзя"
}

// Order — заказ вместе с составом и историей переходов.
type Order struct {
	ID        uuid.UUID
	Number    string
	UserID    uuid.UUID
	VenueID   uuid.UUID
	VenueName string
	Status    Status

	ItemsAmountMinor int64
	DeliveryFeeMinor int64
	TotalAmountMinor int64

	DeliveryAddress string
	DeliveryPoint   Point
	ContactPhone    string
	Comment         string

	ETAMinutes   *int
	CancelReason *string
	WorkflowID   string
	// Fingerprint — отпечаток запроса, которым заказ был оформлен.
	// По нему повтор с тем же ключом отличается от чужого запроса
	// с тем же ключом.
	Fingerprint string

	CreatedAt   time.Time
	PlacedAt    *time.Time
	CompletedAt *time.Time

	Items    []Item
	Timeline []StatusChange
}

// Point — координаты адреса доставки.
type Point struct {
	Lat float64
	Lon float64
}

// Item — позиция заказа: снимок на момент оформления.
//
// Название и цена скопированы намеренно: заведение через час поднимет цену
// или архивирует позицию, а чек выданного заказа меняться не должен —
// ни для пользователя, ни для взаиморасчётов (docs/DESIGN.md §7.2).
type Item struct {
	ID             uuid.UUID
	MenuItemID     uuid.UUID
	ExternalItemID string
	Name           string
	PriceMinor     int64
	Qty            int
	TotalMinor     int64
	Options        []ItemOption
}

// ItemOption — выбранный модификатор в снимке заказа.
type ItemOption struct {
	OptionID        uuid.UUID
	ExternalID      string
	Name            string
	PriceDeltaMinor int64
}

// StatusChange — одна запись истории: кто, когда и почему изменил заказ.
type StatusChange struct {
	FromStatus *Status
	ToStatus   Status
	ActorType  Actor
	Reason     *string
	CreatedAt  time.Time
}

// WorkflowIDFor — идентификатор workflow заказа.
//
// Детерминированный, потому что на нём держится защита от дубля: повторный
// StartWorkflow с тем же идентификатором не создаёт второй процесс
// (docs/DESIGN.md §5).
func WorkflowIDFor(orderID uuid.UUID) string {
	return "order-" + orderID.String()
}
