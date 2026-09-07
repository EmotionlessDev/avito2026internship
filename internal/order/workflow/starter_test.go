package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/events"
	"github.com/emotionlessdev/avito-kitchen/internal/kafkax"
	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	orderworkflow "github.com/emotionlessdev/avito-kitchen/internal/order/workflow"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"
	"go.temporal.io/sdk/testsuite"
)

// quietLogger гасит вывод: страховочный запуск пишет отладочную строку,
// и в тестах этот шум не нужен.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// orderCreatedRecord собирает сообщение так же, как его собрал бы relay.
func orderCreatedRecord(t *testing.T, payload events.OrderCreated) kafkax.Record {
	t.Helper()

	value, err := json.Marshal(payload)
	require.NoError(t, err)

	return kafkax.Record{
		Topic:   kafkax.TopicOrderEvents,
		Value:   value,
		EventID: uuid.New(),
		Type:    events.TypeOrderCreated,
	}
}

func newStarter(t *testing.T) (*orderworkflow.Starter, *mocks.Client) {
	t.Helper()

	temporal := &mocks.Client{}
	process := orderworkflow.NewProcess(temporal, "kitchen-orders", orderworkflow.DefaultTimings())

	return orderworkflow.NewStarter(process, quietLogger()), temporal
}

// Страховочный путь: быстрый синхронный старт не удался, факт оформления
// приехал событием — процесс поднимается отсюда.
func TestStarterStartsProcessFromEvent(t *testing.T) {
	t.Parallel()

	starter, temporal := newStarter(t)
	orderID := uuid.New()

	temporal.On("ExecuteWorkflow", mockAny, mockAny, mockAny, mockAny).
		Return(&mocks.WorkflowRun{}, nil).Once()

	err := starter.Handle(context.Background(), orderCreatedRecord(t, events.OrderCreated{
		OrderID: orderID, Number: "К-260907-0042", UserID: uuid.New(),
		VenueID: uuid.New(), TotalAmountMinor: 131900,
	}))

	require.NoError(t, err)
	temporal.AssertExpectations(t)
}

// Топик общий: события чужого типа старт не запускают.
func TestStarterIgnoresOtherEventTypes(t *testing.T) {
	t.Parallel()

	starter, temporal := newStarter(t)

	record := orderCreatedRecord(t, events.OrderCreated{OrderID: uuid.New()})
	record.Type = events.TypeOrderStatusChanged

	require.NoError(t, starter.Handle(context.Background(), record))
	temporal.AssertNotCalled(t, "ExecuteWorkflow")
}

// Битое тело события — ошибка: молча проглотить его значило бы потерять заказ.
func TestStarterFailsOnBrokenPayload(t *testing.T) {
	t.Parallel()

	starter, _ := newStarter(t)

	err := starter.Handle(context.Background(), kafkax.Record{
		Topic: kafkax.TopicOrderEvents, Value: []byte("не json"),
		EventID: uuid.New(), Type: events.TypeOrderCreated,
	})

	require.Error(t, err)
}

// Процесс уже идёт — это успех, а не ошибка: быстрый путь успел раньше.
// Дубликат заказа невозможен, потому что идентификатор workflow выводится
// из самого заказа.
func TestStarterTreatsAlreadyStartedAsSuccess(t *testing.T) {
	t.Parallel()

	starter, temporal := newStarter(t)

	temporal.On("ExecuteWorkflow", mockAny, mockAny, mockAny, mockAny).
		Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{}).Once()

	err := starter.Handle(context.Background(), orderCreatedRecord(t, events.OrderCreated{
		OrderID: uuid.New(), Number: "К-260907-0043",
	}))

	require.NoError(t, err)
}

// Прочий сбой оркестратора наверх передаётся: событие вернётся повтором.
func TestStarterReportsOtherFailures(t *testing.T) {
	t.Parallel()

	starter, temporal := newStarter(t)

	temporal.On("ExecuteWorkflow", mockAny, mockAny, mockAny, mockAny).
		Return(nil, errors.New("temporal недоступен")).Once()

	err := starter.Handle(context.Background(), orderCreatedRecord(t, events.OrderCreated{
		OrderID: uuid.New(), Number: "К-260907-0044",
	}))

	require.Error(t, err)
}

// Сигналы заведения доходят до процесса по детерминированному идентификатору.
func TestProcessSignalsUseDeterministicWorkflowID(t *testing.T) {
	t.Parallel()

	temporal := &mocks.Client{}
	process := orderworkflow.NewProcess(temporal, "kitchen-orders", orderworkflow.DefaultTimings())
	orderID := uuid.New()
	expected := "order-" + orderID.String()

	temporal.On("SignalWorkflow", mockAny, expected, "", mockAny, mockAny).
		Return(nil).Times(5)

	ctx := context.Background()

	require.NoError(t, process.VenueAccepted(ctx, orderID, 35))
	require.NoError(t, process.VenueRejected(ctx, orderID, "нет продуктов"))
	require.NoError(t, process.Ready(ctx, orderID))
	require.NoError(t, process.Delivered(ctx, orderID))
	require.NoError(t, process.Cancel(ctx, orderID, "передумал"))

	temporal.AssertExpectations(t)
}

// Планировщик стоп-листа тоже адресует процесс по цели, а не по случайному id.
func TestStopListSchedulerUsesTargetWorkflowID(t *testing.T) {
	t.Parallel()

	temporal := &mocks.Client{}
	scheduler := orderworkflow.NewScheduler(temporal, "kitchen-orders")
	target := uuid.New()

	assert.True(t, scheduler.Available())

	temporal.On("SignalWorkflow", mockAny, "stoplist-"+target.String(), "", mockAny, mockAny).
		Return(nil).Once()

	require.NoError(t, scheduler.CancelReturn(context.Background(), merchant.StopTarget{
		Kind: merchant.StopTargetItem, ID: target,
	}))
	temporal.AssertExpectations(t)
}

var _ client.Client = (*mocks.Client)(nil)

// Сверка ставится на расписание постоянным идентификатором, поэтому повторный
// запуск воркера её не дублирует, а «уже идёт» считается успехом.
func TestScheduleReconciliationIsIdempotent(t *testing.T) {
	t.Parallel()

	temporal := &mocks.Client{}
	temporal.On("ExecuteWorkflow", mockAny, mockAny, mockAny).
		Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{}).Once()

	require.NoError(t, orderworkflow.ScheduleReconciliation(
		context.Background(), temporal, "kitchen-orders", quietLogger()))

	failing := &mocks.Client{}
	failing.On("ExecuteWorkflow", mockAny, mockAny, mockAny).
		Return(nil, errors.New("temporal недоступен")).Once()

	require.Error(t, orderworkflow.ScheduleReconciliation(
		context.Background(), failing, "kitchen-orders", quietLogger()))
}

// Сам процесс сверки только зовёт активность и логирует итог: чинить
// расхождения молча он не должен.
func TestMenuReconciliationWorkflowLogsReport(t *testing.T) {
	t.Parallel()

	suiteEnv := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	var activities *orderworkflow.ReconcileActivities

	suiteEnv.RegisterActivity(activities.ReconcileMenus)
	suiteEnv.OnActivity(activities.ReconcileMenus, mockAny).
		Return(orderworkflow.ReconcileReport{VenuesChecked: 3, DriftCount: 2}, nil).Once()

	suiteEnv.ExecuteWorkflow(orderworkflow.MenuReconciliationWorkflow)

	require.True(t, suiteEnv.IsWorkflowCompleted())
	require.NoError(t, suiteEnv.GetWorkflowError())
	suiteEnv.AssertExpectations(t)
}
