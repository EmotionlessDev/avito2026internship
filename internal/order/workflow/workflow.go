// Package workflow — жизненный цикл заказа в Temporal.
//
// Здесь живёт то, ради чего Temporal вообще взят: долгие таймеры, ожидание
// внешних сигналов и компенсации (docs/DESIGN.md §5). Правило пакета —
// детерминизм: в коде workflow нет time.Now, rand, HTTP и SQL, только
// workflow.Now, workflow.NewTimer и активности. Всё, что общается с миром,
// вынесено в activities.go.
package workflow

import (
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/order"
	"github.com/emotionlessdev/avito-kitchen/internal/payments"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	sdk "go.temporal.io/sdk/workflow"
)

// TaskQueue — очередь задач заказов. Одна на воркер: разделять по типам
// пока нечего.
const TaskQueue = "kitchen-orders"

// Имена сигналов. Строки — часть контракта между HTTP-хендлерами и workflow,
// поэтому вынесены в константы и не пишутся руками в двух местах.
const (
	// SignalVenueDecision — заведение приняло или отклонило заказ.
	SignalVenueDecision = "venue_decision"
	// SignalOrderReady — заказ готов к выдаче.
	SignalOrderReady = "order_ready"
	// SignalOrderDelivered — заказ доставлен.
	SignalOrderDelivered = "order_delivered"
	// SignalCancel — пользователь отменил заказ.
	SignalCancel = "cancel"
)

// OrderInput — всё, что нужно процессу заказа. Состав заказа сюда не входит:
// его читает активность из Postgres, а в истории workflow незачем хранить
// копию чека.
type OrderInput struct {
	OrderID          uuid.UUID `json:"order_id"`
	UserID           uuid.UUID `json:"user_id"`
	VenueID          uuid.UUID `json:"venue_id"`
	Number           string    `json:"number"`
	TotalAmountMinor int64     `json:"total_amount_minor"`
	// Timings едут во входе, а не читаются из конфигурации внутри workflow:
	// значения попадают в историю и переживают перезапуск с другими
	// настройками. Читать конфиг в коде workflow нельзя — replay разъедется.
	Timings Timings `json:"timings"`
}

// VenueDecision — решение заведения по заказу.
type VenueDecision struct {
	Accepted   bool   `json:"accepted"`
	ETAMinutes int    `json:"eta_minutes"`
	Reason     string `json:"reason"`
}

// CancelRequest — отмена пользователем.
type CancelRequest struct {
	Reason string `json:"reason"`
}

// Timings — сроки процесса. Вынесены в переменные, потому что тестам нужны
// те же значения, что и проду: durable-таймер проверяется симуляцией времени,
// а не ожиданием семи минут.
type Timings struct {
	// Confirmation — сколько ждём ответа заведения. Не дождались — заказ
	// отменяется сам, а холд возвращается (сценарий U10).
	Confirmation time.Duration
	// CookingSLA — срок, после которого готовка считается затянувшейся.
	// Это только алерт: отменять заказ, который уже готовят, нельзя.
	CookingSLA time.Duration
	// DeliverySLA — то же самое для доставки.
	DeliverySLA time.Duration
}

// DefaultTimings — сроки по умолчанию (docs/DESIGN.md §6.2).
func DefaultTimings() Timings {
	return Timings{
		Confirmation: 7 * time.Minute,
		CookingSLA:   30 * time.Minute,
		DeliverySLA:  45 * time.Minute,
	}
}

// orDefaults подставляет значения по умолчанию вместо незаданных: процессы,
// запущенные до появления этого поля, продолжают работать с прежними сроками.
func (t Timings) orDefaults() Timings {
	defaults := DefaultTimings()

	if t.Confirmation <= 0 {
		t.Confirmation = defaults.Confirmation
	}

	if t.CookingSLA <= 0 {
		t.CookingSLA = defaults.CookingSLA
	}

	if t.DeliverySLA <= 0 {
		t.DeliverySLA = defaults.DeliverySLA
	}

	return t
}

// activityOptions — общие настройки активностей.
//
// Ретраи здесь честные: сеть до POS может моргнуть, база — быть занята.
// Отказ платежа из этого списка исключён отдельно: это не сбой, а решение
// провайдера, и повторять его бессмысленно.
func activityOptions() sdk.ActivityOptions {
	return sdk.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    5,
			NonRetryableErrorTypes: []string{
				errTypePaymentDeclined,
				errTypeInvalidTransition,
			},
		},
	}
}

// OrderWorkflow ведёт заказ от оформления до завершения.
//
// Порядок шагов повторяет docs/DESIGN.md §6.2. Каждая развилка заканчивается
// либо конечным статусом, либо следующим ожиданием: «зависших» состояний
// без таймера или сигнала в процессе нет.
func OrderWorkflow(ctx sdk.Context, input OrderInput) error {
	timings := input.Timings.orDefaults()
	ctx = sdk.WithActivityOptions(ctx, activityOptions())
	log := sdk.GetLogger(ctx)

	var activities *Activities

	// Холд оплаты — первый шаг: если денег нет, заказ не должен доехать
	// до кухни (сценарий U14).
	var hold string

	err := sdk.ExecuteActivity(ctx, activities.AuthorizePayment, AuthorizeInput{
		OrderID:     input.OrderID,
		AmountMinor: input.TotalAmountMinor,
	}).Get(ctx, &hold)
	if err != nil {
		log.Info("холд не прошёл, заказ отменяется", "order", input.Number)

		return setStatus(ctx, input, order.StatusPaymentFailed, order.ActorSystem, "оплата не прошла", nil)
	}

	if sendErr := sdk.ExecuteActivity(ctx, activities.SendOrderToVenue, input.OrderID).Get(ctx, nil); sendErr != nil {
		// Заведение недоступно даже после ретраев — заказ не потерян:
		// он останется в ленте, и POS заберёт его поллингом (сценарий M6).
		log.Warn("заказ не доставлен вебхуком, остаётся на поллинг", "order", input.Number)
	}

	if statusErr := setStatus(ctx, input, order.StatusPlaced, order.ActorSystem, "", nil); statusErr != nil {
		return statusErr
	}

	decision, err := waitForVenue(ctx, input, timings)
	if err != nil {
		return err
	}

	switch decision {
	case outcomeAccepted:
		return cookAndDeliver(ctx, input, timings)
	case outcomeRejected, outcomeCancelled, outcomeExpired:
		return nil
	default:
		return nil
	}
}

// outcome — чем закончилось ожидание заведения.
type outcome int

const (
	outcomeAccepted outcome = iota
	outcomeRejected
	outcomeCancelled
	outcomeExpired
)

// waitForVenue ждёт решения заведения, отмены пользователем или таймаута.
//
// Селектор, а не последовательные ожидания: все три события равноправны,
// и выигрывает то, что случилось первым.
func waitForVenue(ctx sdk.Context, input OrderInput, timings Timings) (outcome, error) {
	var (
		decision  VenueDecision
		cancelled CancelRequest
		result    outcome
		failure   error
	)

	timer, cancelTimer := sdk.WithCancel(ctx)
	defer cancelTimer()

	selector := sdk.NewSelector(ctx)

	selector.AddReceive(sdk.GetSignalChannel(ctx, SignalVenueDecision), func(c sdk.ReceiveChannel, _ bool) {
		c.Receive(ctx, &decision)

		if !decision.Accepted {
			result = outcomeRejected
			failure = compensate(ctx, input, order.StatusRejected, order.ActorVenue, decision.Reason)

			return
		}

		result = outcomeAccepted
		eta := decision.ETAMinutes
		failure = setStatus(ctx, input, order.StatusAccepted, order.ActorVenue, "", &eta)
	})

	selector.AddReceive(sdk.GetSignalChannel(ctx, SignalCancel), func(c sdk.ReceiveChannel, _ bool) {
		c.Receive(ctx, &cancelled)

		result = outcomeCancelled
		failure = compensate(ctx, input, order.StatusCancelled, order.ActorUser, cancelled.Reason)
	})

	selector.AddFuture(sdk.NewTimer(timer, timings.Confirmation), func(sdk.Future) {
		result = outcomeExpired
		failure = compensate(ctx, input, order.StatusExpired, order.ActorSystem,
			"заведение не подтвердило заказ")
	})

	selector.Select(ctx)

	return result, failure
}

// cookAndDeliver ведёт принятый заказ до завершения.
func cookAndDeliver(ctx sdk.Context, input OrderInput, timings Timings) error {
	if err := setStatus(ctx, input, order.StatusCooking, order.ActorSystem, "", nil); err != nil {
		return err
	}

	ready, err := waitForReady(ctx, input, timings)
	if err != nil || !ready {
		return err
	}

	if err := setStatus(ctx, input, order.StatusReady, order.ActorVenue, "", nil); err != nil {
		return err
	}

	var activities *Activities
	if err := sdk.ExecuteActivity(ctx, activities.AssignCourier, input.OrderID).Get(ctx, nil); err != nil {
		return err
	}

	if err := setStatus(ctx, input, order.StatusDelivering, order.ActorSystem, "", nil); err != nil {
		return err
	}

	if err := waitForDelivery(ctx, input, timings); err != nil {
		return err
	}

	if err := sdk.ExecuteActivity(ctx, activities.CapturePayment, input.OrderID).Get(ctx, nil); err != nil {
		return err
	}

	return setStatus(ctx, input, order.StatusCompleted, order.ActorSystem, "", nil)
}

// waitForReady ждёт готовности блюда. Второй возвращаемый признак — «заказ
// поехал дальше»: false означает, что пользователь успел отменить.
//
// SLA готовки здесь только алерт: заказ, который уже готовят, отменять нельзя,
// а вот знать, что кухня выбилась из срока, полезно.
func waitForReady(ctx sdk.Context, input OrderInput, timings Timings) (bool, error) {
	var (
		cancelled CancelRequest
		proceed   bool
		stopped   bool
		failure   error
	)

	log := sdk.GetLogger(ctx)

	timer, cancelTimer := sdk.WithCancel(ctx)
	defer cancelTimer()

	slaFired := false

	// Выход из цикла — только по трём причинам: заказ готов, заказ отменён
	// или компенсация упала. Отдельный флаг вместо «ошибка не nil» нужен
	// потому, что успешная отмена ошибкой не является.
	for !proceed && !stopped && failure == nil {
		selector := sdk.NewSelector(ctx)

		selector.AddReceive(sdk.GetSignalChannel(ctx, SignalOrderReady), func(c sdk.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)

			proceed = true
		})

		selector.AddReceive(sdk.GetSignalChannel(ctx, SignalCancel), func(c sdk.ReceiveChannel, _ bool) {
			c.Receive(ctx, &cancelled)

			stopped = true
			failure = compensate(ctx, input, order.StatusCancelled, order.ActorUser, cancelled.Reason)
		})

		if !slaFired {
			selector.AddFuture(sdk.NewTimer(timer, timings.CookingSLA), func(sdk.Future) {
				slaFired = true

				log.Warn("готовка выбилась из SLA", "order", input.Number)
			})
		}

		selector.Select(ctx)
	}

	return proceed, failure
}

// waitForDelivery ждёт подтверждения доставки. Отмена здесь уже невозможна —
// машина состояний её не разрешает (сценарий U11).
func waitForDelivery(ctx sdk.Context, input OrderInput, timings Timings) error {
	log := sdk.GetLogger(ctx)

	timer, cancelTimer := sdk.WithCancel(ctx)
	defer cancelTimer()

	delivered := false
	slaFired := false

	for !delivered {
		selector := sdk.NewSelector(ctx)

		selector.AddReceive(sdk.GetSignalChannel(ctx, SignalOrderDelivered), func(c sdk.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)

			delivered = true
		})

		if !slaFired {
			selector.AddFuture(sdk.NewTimer(timer, timings.DeliverySLA), func(sdk.Future) {
				slaFired = true

				log.Warn("доставка выбилась из SLA", "order", input.Number)
			})
		}

		selector.Select(ctx)
	}

	return nil
}

// compensate возвращает холд и переводит заказ в конечный статус.
//
// Порядок важен: сначала деньги, потом статус. Если упасть между шагами,
// повтор активности вернёт холд ещё раз — это безопасно, а вот заказ,
// отменённый с зависшими деньгами, безопасным не был бы.
func compensate(ctx sdk.Context, input OrderInput, to order.Status, actor order.Actor, reason string) error {
	var activities *Activities

	if err := sdk.ExecuteActivity(ctx, activities.VoidPayment, input.OrderID).Get(ctx, nil); err != nil {
		return err
	}

	return setStatus(ctx, input, to, actor, reason, nil)
}

func setStatus(
	ctx sdk.Context, input OrderInput, to order.Status,
	actor order.Actor, reason string, eta *int,
) error {
	var activities *Activities

	return sdk.ExecuteActivity(ctx, activities.SetStatus, SetStatusInput{
		OrderID:    input.OrderID,
		To:         to,
		Actor:      actor,
		Reason:     reason,
		ETAMinutes: eta,
	}).Get(ctx, nil)
}

// Типы ошибок, которые не имеет смысла повторять.
const (
	errTypePaymentDeclined   = "PaymentDeclined"
	errTypeInvalidTransition = "InvalidTransition"
)

// declinedError оборачивает отказ провайдера так, чтобы Temporal не ретраил:
// банк не передумает от повторной попытки.
func declinedError(err error) error {
	return temporal.NewNonRetryableApplicationError(
		"платёж отклонён", errTypePaymentDeclined, err)
}

// transitionError помечает недопустимый переход: повторять его тоже
// бессмысленно, состояние уже другое.
func transitionError(err error) error {
	return temporal.NewNonRetryableApplicationError(
		"недопустимый переход статуса", errTypeInvalidTransition, err)
}

// ensure — компиляционная проверка, что мок платежей удовлетворяет интерфейсу
// провайдера: активности зависят от интерфейса, а не от конкретного мока.
var _ payments.Provider = payments.Mock{}
