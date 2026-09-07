// Package payments — платёжный провайдер платформы.
//
// В MVP провайдера нет: нет ни договора, ни PCI-контура (README.md, раздел «Осознанные упрощения»).
// Но механика холдов настоящая — авторизация при оформлении, списание при
// доставке, отмена холда при отказе заведения. Заменить мок на реального
// провайдера значит реализовать этот же интерфейс из трёх методов.
package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Статусы платежа. Значения совпадают с CHECK в миграции 0003_orders.
const (
	StatusAuthorized = "authorized"
	StatusCaptured   = "captured"
	StatusVoided     = "voided"
)

// ErrDeclined — банк отказал в холде. Отдельная ошибка, а не общий сбой:
// отказ — это штатный исход, по которому заказ уходит в payment_failed,
// а не повод ретраить (сценарий U14).
var ErrDeclined = errors.New("платёж отклонён")

// Provider — то, что платформа ждёт от платёжного провайдера.
type Provider interface {
	// Authorize замораживает сумму. Ключ идемпотентности — сам заказ:
	// повторный вызов для того же заказа обязан вернуть тот же холд.
	Authorize(ctx context.Context, orderID uuid.UUID, amountMinor int64) (string, error)
	// Capture списывает ранее замороженную сумму.
	Capture(ctx context.Context, providerRef string) error
	// Void снимает холд — компенсация при отказе заведения или таймауте.
	Void(ctx context.Context, providerRef string) error
}

// declineRemainder — остаток суммы в копейках, на котором мок отказывает.
//
// Отказ детерминированный намеренно: случайные отказы сделали бы
// недетерминированными и демо, и replay workflow. Сумма, оканчивающаяся
// на 99 копеек, — это «карта, по которой платёж не проходит» из сценария U14.
const declineRemainder = 99

// Mock — платёжный провайдер для стенда.
type Mock struct{}

// NewMock собирает мок-провайдера.
func NewMock() Mock { return Mock{} }

// Authorize замораживает сумму, отказывая на «несчастливых» суммах.
func (Mock) Authorize(_ context.Context, orderID uuid.UUID, amountMinor int64) (string, error) {
	if amountMinor%100 == declineRemainder {
		return "", fmt.Errorf("%w: сумма %d копеек", ErrDeclined, amountMinor)
	}

	// Ссылка выводится из заказа, а не генерируется случайно: повторный
	// вызов активности вернёт тот же холд, как и настоящий провайдер
	// с ключом идемпотентности.
	return "mock-hold-" + orderID.String(), nil
}

// Capture списывает холд.
func (Mock) Capture(_ context.Context, providerRef string) error {
	if providerRef == "" {
		return fmt.Errorf("%w: нечего списывать", ErrDeclined)
	}

	return nil
}

// Void снимает холд.
func (Mock) Void(_ context.Context, providerRef string) error {
	if providerRef == "" {
		return fmt.Errorf("%w: нечего отменять", ErrDeclined)
	}

	return nil
}
