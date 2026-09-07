package workflow_test

import (
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/order"
	orderworkflow "github.com/emotionlessdev/avito-kitchen/internal/order/workflow"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// OrderWorkflowSuite проверяет пять веток жизненного цикла заказа.
//
// Тестовое окружение Temporal двигает время само, поэтому семиминутный таймер
// подтверждения проходит за миллисекунды — ровно ради этого durable-таймеры
// и взяты вместо cron-таблицы (docs/DESIGN.md §5).
type OrderWorkflowSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite

	env        *testsuite.TestWorkflowEnvironment
	activities *orderworkflow.Activities
}

func TestOrderWorkflowSuite(t *testing.T) {
	suite.Run(t, new(OrderWorkflowSuite))
}

func (s *OrderWorkflowSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
	// Активности подменяются моками: workflow проверяется как процесс,
	// а не как обёртка над базой. Настоящие активности покрываются
	// интеграционными тестами.
	s.activities = &orderworkflow.Activities{}
	s.env.RegisterActivity(s.activities.AuthorizePayment)
	s.env.RegisterActivity(s.activities.SendOrderToVenue)
	s.env.RegisterActivity(s.activities.SetStatus)
	s.env.RegisterActivity(s.activities.CapturePayment)
	s.env.RegisterActivity(s.activities.VoidPayment)
	s.env.RegisterActivity(s.activities.AssignCourier)
}

func (s *OrderWorkflowSuite) AfterTest(_, _ string) {
	s.env.AssertExpectations(s.T())
}

func input() orderworkflow.OrderInput {
	return orderworkflow.OrderInput{
		OrderID:          uuid.MustParse("01890000-0100-7000-8000-000000000001"),
		UserID:           uuid.MustParse("01890000-0000-7000-8000-000000000001"),
		VenueID:          uuid.MustParse("01890000-0001-7000-8000-000000000001"),
		Number:           "К-260907-0042",
		TotalAmountMinor: 118900,
	}
}

// expectStatus объявляет ожидаемый переход: тест читается как список
// статусов, через которые обязан пройти заказ.
func (s *OrderWorkflowSuite) expectStatus(to order.Status, actor order.Actor) {
	s.env.OnActivity(s.activities.SetStatus, mockAny, mockStatus(to, actor)).
		Return(nil).Once()
}

// U1 — happy path: заведение приняло, приготовило, курьер доставил.
func (s *OrderWorkflowSuite) TestAcceptedOrderReachesCompleted() {
	s.env.OnActivity(s.activities.AuthorizePayment, mockAny, mockAny).
		Return("mock-hold", nil).Once()
	s.env.OnActivity(s.activities.SendOrderToVenue, mockAny, mockAny).Return(nil).Once()
	s.env.OnActivity(s.activities.AssignCourier, mockAny, mockAny).Return("courier", nil).Once()
	s.env.OnActivity(s.activities.CapturePayment, mockAny, mockAny).Return(nil).Once()

	s.expectStatus(order.StatusPlaced, order.ActorSystem)
	s.expectStatus(order.StatusAccepted, order.ActorVenue)
	s.expectStatus(order.StatusCooking, order.ActorSystem)
	s.expectStatus(order.StatusReady, order.ActorVenue)
	s.expectStatus(order.StatusDelivering, order.ActorSystem)
	s.expectStatus(order.StatusCompleted, order.ActorSystem)

	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(orderworkflow.SignalVenueDecision,
			orderworkflow.VenueDecision{Accepted: true, ETAMinutes: 35})
	}, time.Minute)

	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(orderworkflow.SignalOrderReady, nil)
	}, 20*time.Minute)

	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(orderworkflow.SignalOrderDelivered, nil)
	}, 40*time.Minute)

	s.env.ExecuteWorkflow(orderworkflow.OrderWorkflow, input())

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
}

// M5 — заведение отказало: холд возвращается, заказ уходит в rejected.
func (s *OrderWorkflowSuite) TestRejectedOrderVoidsPayment() {
	s.env.OnActivity(s.activities.AuthorizePayment, mockAny, mockAny).
		Return("mock-hold", nil).Once()
	s.env.OnActivity(s.activities.SendOrderToVenue, mockAny, mockAny).Return(nil).Once()
	s.env.OnActivity(s.activities.VoidPayment, mockAny, mockAny).Return(nil).Once()

	s.expectStatus(order.StatusPlaced, order.ActorSystem)
	s.expectStatus(order.StatusRejected, order.ActorVenue)

	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(orderworkflow.SignalVenueDecision,
			orderworkflow.VenueDecision{Accepted: false, Reason: "закончились продукты"})
	}, time.Minute)

	s.env.ExecuteWorkflow(orderworkflow.OrderWorkflow, input())

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
}

// U10 — заведение молчит: семиминутный таймер отменяет заказ сам.
// В тестовом окружении это занимает миллисекунды.
func (s *OrderWorkflowSuite) TestConfirmationTimeoutExpiresOrder() {
	s.env.OnActivity(s.activities.AuthorizePayment, mockAny, mockAny).
		Return("mock-hold", nil).Once()
	s.env.OnActivity(s.activities.SendOrderToVenue, mockAny, mockAny).Return(nil).Once()
	s.env.OnActivity(s.activities.VoidPayment, mockAny, mockAny).Return(nil).Once()

	s.expectStatus(order.StatusPlaced, order.ActorSystem)
	s.expectStatus(order.StatusExpired, order.ActorSystem)

	started := time.Now()

	s.env.ExecuteWorkflow(orderworkflow.OrderWorkflow, input())

	s.True(s.env.IsWorkflowCompleted())
	s.Require().NoError(s.env.GetWorkflowError())
	s.Less(time.Since(started), 5*time.Second,
		"семиминутный таймер обязан проходить за миллисекунды симуляции")
}

// U11 — пользователь отменил заказ до подтверждения.
func (s *OrderWorkflowSuite) TestUserCancelBeforeConfirmation() {
	s.env.OnActivity(s.activities.AuthorizePayment, mockAny, mockAny).
		Return("mock-hold", nil).Once()
	s.env.OnActivity(s.activities.SendOrderToVenue, mockAny, mockAny).Return(nil).Once()
	s.env.OnActivity(s.activities.VoidPayment, mockAny, mockAny).Return(nil).Once()

	s.expectStatus(order.StatusPlaced, order.ActorSystem)
	s.expectStatus(order.StatusCancelled, order.ActorUser)

	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(orderworkflow.SignalCancel,
			orderworkflow.CancelRequest{Reason: "передумал"})
	}, 2*time.Minute)

	s.env.ExecuteWorkflow(orderworkflow.OrderWorkflow, input())

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
}

// U11 — отмена во время готовки: заказ ещё можно вернуть.
func (s *OrderWorkflowSuite) TestUserCancelWhileCooking() {
	s.env.OnActivity(s.activities.AuthorizePayment, mockAny, mockAny).
		Return("mock-hold", nil).Once()
	s.env.OnActivity(s.activities.SendOrderToVenue, mockAny, mockAny).Return(nil).Once()
	s.env.OnActivity(s.activities.VoidPayment, mockAny, mockAny).Return(nil).Once()

	s.expectStatus(order.StatusPlaced, order.ActorSystem)
	s.expectStatus(order.StatusAccepted, order.ActorVenue)
	s.expectStatus(order.StatusCooking, order.ActorSystem)
	s.expectStatus(order.StatusCancelled, order.ActorUser)

	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(orderworkflow.SignalVenueDecision,
			orderworkflow.VenueDecision{Accepted: true, ETAMinutes: 35})
	}, time.Minute)

	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(orderworkflow.SignalCancel,
			orderworkflow.CancelRequest{Reason: "уже не нужно"})
	}, 5*time.Minute)

	s.env.ExecuteWorkflow(orderworkflow.OrderWorkflow, input())

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
}

// U14 — холд не прошёл: заказ не уезжает в заведение вовсе.
func (s *OrderWorkflowSuite) TestDeclinedPaymentStopsOrder() {
	s.env.OnActivity(s.activities.AuthorizePayment, mockAny, mockAny).
		Return("", temporal.NewNonRetryableApplicationError(
			"платёж отклонён", "PaymentDeclined", nil)).Once()

	s.expectStatus(order.StatusPaymentFailed, order.ActorSystem)

	s.env.ExecuteWorkflow(orderworkflow.OrderWorkflow, input())

	s.True(s.env.IsWorkflowCompleted())
	s.Require().NoError(s.env.GetWorkflowError())
	// Once() выше — это и проверка политики ретраев: отказ провайдера
	// помечен неповторяемым, поэтому активность вызывается ровно один раз.
	s.env.AssertNotCalled(s.T(), "SendOrderToVenue", mockAny, mockAny)
}
