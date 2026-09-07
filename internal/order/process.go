package order

import (
	"context"

	"github.com/google/uuid"
)

// ProcessInput — то, что нужно запустить процесс заказа.
//
// Тип объявлен здесь, а не в пакете workflow, чтобы домен не зависел
// от Temporal: сервис знает, что у заказа есть процесс, но не знает, чем
// он исполняется.
type ProcessInput struct {
	OrderID          uuid.UUID
	UserID           uuid.UUID
	VenueID          uuid.UUID
	Number           string
	TotalAmountMinor int64
}

// Process — процесс заказа глазами домена: его можно запустить и ему можно
// сообщить о случившемся. Реализация живёт в internal/order/workflow.
//
// Все методы обязаны быть идемпотентными: HTTP-запрос может прийти повторно,
// а сигнал — продублироваться.
type Process interface {
	Start(ctx context.Context, input ProcessInput) error
	VenueAccepted(ctx context.Context, orderID uuid.UUID, etaMinutes int) error
	VenueRejected(ctx context.Context, orderID uuid.UUID, reason string) error
	Ready(ctx context.Context, orderID uuid.UUID) error
	Delivered(ctx context.Context, orderID uuid.UUID) error
	Cancel(ctx context.Context, orderID uuid.UUID, reason string) error
}

// NoProcess — заглушка на случай, когда оркестратор не подключён.
//
// Нужна тестам и локальному запуску без Temporal: заказ создаётся и виден
// в API, просто не едет по статусам.
type NoProcess struct{}

// Start ничего не делает.
func (NoProcess) Start(context.Context, ProcessInput) error { return nil }

// VenueAccepted ничего не делает.
func (NoProcess) VenueAccepted(context.Context, uuid.UUID, int) error { return nil }

// VenueRejected ничего не делает.
func (NoProcess) VenueRejected(context.Context, uuid.UUID, string) error { return nil }

// Ready ничего не делает.
func (NoProcess) Ready(context.Context, uuid.UUID) error { return nil }

// Delivered ничего не делает.
func (NoProcess) Delivered(context.Context, uuid.UUID) error { return nil }

// Cancel ничего не делает.
func (NoProcess) Cancel(context.Context, uuid.UUID, string) error { return nil }
