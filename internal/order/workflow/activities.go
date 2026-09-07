package workflow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/order"
	"github.com/emotionlessdev/avito-kitchen/internal/payments"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
)

// Activities — всё, что процесс заказа делает во внешнем мире: платежи,
// вызовы заведения, запись статуса.
//
// Отдельный тип, а не функции: у активностей общие зависимости, и держать
// их в глобальных переменных значило бы лишить тесты возможности подменять
// провайдера.
type Activities struct {
	orders   *order.Repository
	payments payments.Provider
	client   *http.Client
}

// NewActivities собирает активности заказа.
func NewActivities(orders *order.Repository, provider payments.Provider) *Activities {
	return &Activities{
		orders:   orders,
		payments: provider,
		client: &http.Client{
			// Таймаут короче, чем StartToCloseTimeout активности: пусть
			// повтором управляет Temporal, а не зависший сокет.
			Timeout: 10 * time.Second,
		},
	}
}

// AuthorizeInput — параметры холда.
type AuthorizeInput struct {
	OrderID     uuid.UUID `json:"order_id"`
	AmountMinor int64     `json:"amount_minor"`
}

// AuthorizePayment замораживает сумму заказа.
//
// Идемпотентность обеспечивает провайдер: ключ холда выводится из заказа,
// поэтому повторный вызов активности не создаёт второй холд.
func (a *Activities) AuthorizePayment(ctx context.Context, input AuthorizeInput) (string, error) {
	ref, err := a.payments.Authorize(ctx, input.OrderID, input.AmountMinor)
	if err != nil {
		if errors.Is(err, payments.ErrDeclined) {
			return "", declinedError(err)
		}

		return "", fmt.Errorf("холд оплаты: %w", err)
	}

	if err := a.orders.SavePayment(ctx, input.OrderID, payments.StatusAuthorized,
		input.AmountMinor, ref); err != nil {
		return "", err
	}

	return ref, nil
}

// CapturePayment списывает холд после доставки.
func (a *Activities) CapturePayment(ctx context.Context, orderID uuid.UUID) error {
	ref, err := a.orders.PaymentRef(ctx, orderID)
	if err != nil {
		return err
	}

	if ref == "" {
		// Холда не было — списывать нечего. Это не ошибка: заказ мог
		// дойти сюда после ручного вмешательства.
		return nil
	}

	if err := a.payments.Capture(ctx, ref); err != nil {
		return fmt.Errorf("списание холда: %w", err)
	}

	return a.orders.SavePayment(ctx, orderID, payments.StatusCaptured, 0, ref)
}

// VoidPayment снимает холд — компенсация при отказе, отмене или таймауте.
func (a *Activities) VoidPayment(ctx context.Context, orderID uuid.UUID) error {
	ref, err := a.orders.PaymentRef(ctx, orderID)
	if err != nil {
		return err
	}

	if ref == "" {
		return nil
	}

	if err := a.payments.Void(ctx, ref); err != nil {
		return fmt.Errorf("возврат холда: %w", err)
	}

	return a.orders.SavePayment(ctx, orderID, payments.StatusVoided, 0, ref)
}

// SetStatusInput — параметры перехода.
type SetStatusInput struct {
	OrderID    uuid.UUID    `json:"order_id"`
	To         order.Status `json:"to"`
	Actor      order.Actor  `json:"actor"`
	Reason     string       `json:"reason"`
	ETAMinutes *int         `json:"eta_minutes,omitempty"`
}

// SetStatus переводит заказ в новый статус.
//
// Именно эта активность делает orders.status проекцией состояния workflow:
// больше никто статус не пишет (главный ADR, docs/DESIGN.md §5).
func (a *Activities) SetStatus(ctx context.Context, input SetStatusInput) error {
	_, err := a.orders.ApplyStatus(ctx, order.Transition{
		OrderID:    input.OrderID,
		To:         input.To,
		Actor:      input.Actor,
		Reason:     input.Reason,
		ETAMinutes: input.ETAMinutes,
	})

	var invalid order.ErrInvalidTransition
	if errors.As(err, &invalid) {
		// Состояние уже другое — повторять бессмысленно.
		return transitionError(err)
	}

	var forbidden order.ErrForbiddenActor
	if errors.As(err, &forbidden) {
		return transitionError(err)
	}

	return err
}

// AssignCourier назначает курьера.
//
// Курьерская логистика — отдельный домен размером с этот проект (§11),
// поэтому здесь заглушка: она нужна, чтобы в процессе был явный шаг,
// который потом заменит вызов настоящего сервиса.
func (a *Activities) AssignCourier(ctx context.Context, orderID uuid.UUID) (string, error) {
	activity.GetLogger(ctx).Info("курьер назначен (мок)", "order_id", orderID.String())

	return "courier-mock", nil
}

// SendOrderToVenue доставляет заказ в систему заведения.
//
// Именно поэтому доставка в POS — активность Temporal, а не консьюмер Kafka:
// процессу нужен результат вызова, чтобы решать, что делать дальше
// (docs/DESIGN.md §5).
func (a *Activities) SendOrderToVenue(ctx context.Context, orderID uuid.UUID) error {
	current, err := a.orders.ByID(ctx, orderID)
	if err != nil {
		return err
	}

	webhook, err := a.orders.VenueWebhook(ctx, current.VenueID)
	if err != nil {
		return err
	}

	if webhook.URL == "" {
		// Заведение не регистрировало вебхук — заберёт заказ поллингом
		// (сценарий M6). Это штатный путь, а не ошибка.
		activity.GetLogger(ctx).Info("вебхук не зарегистрирован, заказ ждёт поллинга",
			"order", current.Number)

		return nil
	}

	return a.postWebhook(ctx, webhook, current)
}

func (a *Activities) postWebhook(ctx context.Context, webhook order.Webhook, current order.Order) error {
	eventID := uuid.New()
	timestamp := time.Now().UTC().Format(time.RFC3339)

	body, err := json.Marshal(orderCreatedEvent(eventID, current))
	if err != nil {
		return fmt.Errorf("сериализация заказа для заведения: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("запрос в заведение: %w", err)
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Kitchen-Event-Id", eventID.String())
	request.Header.Set("X-Kitchen-Timestamp", timestamp)
	request.Header.Set("X-Kitchen-Signature", sign(webhook.Secret, timestamp, body))

	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("вызов заведения: %w", err)
	}

	defer func() { _ = response.Body.Close() }()

	// 409 — заведение уже видело это событие. Для нас это успех: доставка
	// at-least-once, и повтор — норма.
	if response.StatusCode == http.StatusConflict || response.StatusCode/100 == 2 {
		return nil
	}

	return fmt.Errorf("заведение ответило %d: %w", response.StatusCode, errWebhookRejected)
}

var errWebhookRejected = errors.New("вебхук не принят заведением")

// sign считает подпись вебхука: HMAC-SHA256 от «время.тело».
//
// Время входит в подпись вместе с телом, поэтому перехваченный запрос нельзя
// переиграть позже: заведение проверяет и подпись, и окно времени
// (docs/DESIGN.md §10).
func sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)

	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
