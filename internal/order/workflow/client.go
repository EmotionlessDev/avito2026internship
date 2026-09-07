package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/order"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

// isAlreadyStarted отличает «процесс уже идёт» от настоящей ошибки запуска.
func isAlreadyStarted(err error) bool {
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted

	return errors.As(err, &alreadyStarted)
}

// Process запускает процессы заказов и шлёт им сигналы. Реализует
// order.Process, поэтому домен остаётся независимым от Temporal.
type Process struct {
	temporal  client.Client
	taskQueue string
	timings   Timings
}

// NewProcess собирает клиента процессов.
//
// Сроки задаются здесь и уезжают во вход workflow: так их можно ускорить
// для демо и e2e, не трогая код процесса и не ломая детерминизм.
func NewProcess(temporal client.Client, taskQueue string, timings Timings) *Process {
	return &Process{temporal: temporal, taskQueue: taskQueue, timings: timings}
}

// Start запускает процесс заказа.
//
// Идентификатор workflow выводится из заказа, а политика повторного
// использования запрещает дубль: два запроса на один заказ дадут один процесс,
// сколько бы раз ни повторился вызов (docs/DESIGN.md §5).
func (p *Process) Start(ctx context.Context, input order.ProcessInput) error {
	options := client.StartWorkflowOptions{
		ID:        order.WorkflowIDFor(input.OrderID),
		TaskQueue: p.taskQueue,
		// Дубликат заказа физически невозможен: повторный старт с тем же
		// идентификатором вернёт уже идущий процесс.
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}

	_, err := p.temporal.ExecuteWorkflow(ctx, options, OrderWorkflow, OrderInput{
		OrderID:          input.OrderID,
		UserID:           input.UserID,
		VenueID:          input.VenueID,
		Number:           input.Number,
		TotalAmountMinor: input.TotalAmountMinor,
		Timings:          p.timings,
	})
	if err != nil {
		if isAlreadyStarted(err) {
			// Процесс уже идёт: это повтор быстрого пути или страховочный
			// запуск из очереди событий.
			return nil
		}

		return fmt.Errorf("запуск процесса заказа: %w", err)
	}

	return nil
}

// VenueAccepted сообщает процессу, что заведение приняло заказ.
func (p *Process) VenueAccepted(ctx context.Context, orderID uuid.UUID, etaMinutes int) error {
	return p.signal(ctx, orderID, SignalVenueDecision,
		VenueDecision{Accepted: true, ETAMinutes: etaMinutes})
}

// VenueRejected сообщает об отказе заведения.
func (p *Process) VenueRejected(ctx context.Context, orderID uuid.UUID, reason string) error {
	return p.signal(ctx, orderID, SignalVenueDecision,
		VenueDecision{Accepted: false, Reason: reason})
}

// Ready сообщает о готовности заказа.
func (p *Process) Ready(ctx context.Context, orderID uuid.UUID) error {
	return p.signal(ctx, orderID, SignalOrderReady, nil)
}

// Delivered сообщает о доставке.
func (p *Process) Delivered(ctx context.Context, orderID uuid.UUID) error {
	return p.signal(ctx, orderID, SignalOrderDelivered, nil)
}

// Cancel сообщает об отмене пользователем.
func (p *Process) Cancel(ctx context.Context, orderID uuid.UUID, reason string) error {
	return p.signal(ctx, orderID, SignalCancel, CancelRequest{Reason: reason})
}

func (p *Process) signal(ctx context.Context, orderID uuid.UUID, name string, payload any) error {
	if err := p.temporal.SignalWorkflow(ctx, order.WorkflowIDFor(orderID), "", name, payload); err != nil {
		return fmt.Errorf("сигнал %s: %w", name, err)
	}

	return nil
}

// Scheduler ставит и снимает таймеры возврата из стоп-листа. Реализует
// merchant.StopListScheduler — тот самый шов, заведённый на этапе 4.
type Scheduler struct {
	temporal  client.Client
	taskQueue string
}

// NewScheduler собирает планировщик стоп-листа.
func NewScheduler(temporal client.Client, taskQueue string) *Scheduler {
	return &Scheduler{temporal: temporal, taskQueue: taskQueue}
}

// Available сообщает, что durable-таймеры доступны.
func (s *Scheduler) Available() bool { return true }

// ScheduleReturn ставит возврат позиции в продажу на указанный момент.
//
// Повторный вызов для той же цели перезапускает таймер: идентификатор
// процесса детерминирован, а политика позволяет заменить предыдущий.
func (s *Scheduler) ScheduleReturn(
	ctx context.Context, target merchant.StopTarget, until time.Time,
) error {
	venueID, err := merchant.TenantFrom(ctx)
	if err != nil {
		return err
	}

	options := client.StartWorkflowOptions{
		ID:        StopListWorkflowID(target.ID),
		TaskQueue: s.taskQueue,
		// Повторное снятие той же позиции перезапускает таймер: прежний
		// процесс закрывается, новый считает срок заново.
		WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
	}

	_, err = s.temporal.ExecuteWorkflow(ctx, options, StopListWorkflow, StopListInput{
		VenueID:  venueID,
		Kind:     target.Kind,
		TargetID: target.ID,
		Until:    until,
	})
	if err != nil {
		return fmt.Errorf("планирование возврата в продажу: %w", err)
	}

	return nil
}

// CancelReturn снимает запланированный возврат: заведение вернуло позицию
// раньше срока.
func (s *Scheduler) CancelReturn(ctx context.Context, target merchant.StopTarget) error {
	// Ошибка сигнала здесь ожидаема и не является сбоем: процесса может
	// не быть вовсе, если позицию снимали без срока возврата. Отменять
	// в таком случае нечего.
	_ = s.temporal.SignalWorkflow(ctx, StopListWorkflowID(target.ID), "", SignalStopListCancel, nil)

	return nil
}
