package workflow_test

import (
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	orderworkflow "github.com/emotionlessdev/avito-kitchen/internal/order/workflow"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/testsuite"
)

type StopListWorkflowSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite

	env        *testsuite.TestWorkflowEnvironment
	activities *orderworkflow.StopListActivities
}

func TestStopListWorkflowSuite(t *testing.T) {
	suite.Run(t, new(StopListWorkflowSuite))
}

func (s *StopListWorkflowSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
	s.activities = &orderworkflow.StopListActivities{}
	s.env.RegisterActivity(s.activities.ReturnToSale)
}

func (s *StopListWorkflowSuite) AfterTest(_, _ string) {
	s.env.AssertExpectations(s.T())
}

func stopListInput(until time.Time) orderworkflow.StopListInput {
	return orderworkflow.StopListInput{
		VenueID:  uuid.MustParse("01890000-0001-7000-8000-000000000001"),
		Kind:     merchant.StopTargetItem,
		TargetID: uuid.MustParse("01890000-0003-7000-8000-000000000104"),
		Until:    until,
	}
}

// M3 — заведение сняло позицию «до вечера»: процесс спит нужное время
// и возвращает её сам. Восемь часов проходят за миллисекунды симуляции —
// ровно ради этого стоп-лист живёт в Temporal, а не в колонке таблицы.
func (s *StopListWorkflowSuite) TestReturnsToSaleWhenTimerFires() {
	s.env.OnActivity(s.activities.ReturnToSale, mockAny, mockAny).Return(nil).Once()

	started := time.Now()

	s.env.ExecuteWorkflow(orderworkflow.StopListWorkflow,
		stopListInput(s.env.Now().Add(8*time.Hour)))

	s.True(s.env.IsWorkflowCompleted())
	s.Require().NoError(s.env.GetWorkflowError())
	s.Less(time.Since(started), 5*time.Second,
		"восьмичасовой таймер обязан проходить за миллисекунды симуляции")
}

// Заведение вернуло позицию раньше срока — таймер отменяется, и повторный
// возврат в продажу не выполняется.
func (s *StopListWorkflowSuite) TestCancelSignalStopsScheduledReturn() {
	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(orderworkflow.SignalStopListCancel, nil)
	}, time.Hour)

	s.env.ExecuteWorkflow(orderworkflow.StopListWorkflow,
		stopListInput(s.env.Now().Add(8*time.Hour)))

	s.True(s.env.IsWorkflowCompleted())
	s.Require().NoError(s.env.GetWorkflowError())
}

// Момент возврата уже прошёл — возвращаем сразу, а не отказываем: заведение
// просило вернуть «к этому времени», и оно наступило.
func (s *StopListWorkflowSuite) TestReturnsImmediatelyWhenDeadlinePassed() {
	s.env.OnActivity(s.activities.ReturnToSale, mockAny, mockAny).Return(nil).Once()

	s.env.ExecuteWorkflow(orderworkflow.StopListWorkflow,
		stopListInput(s.env.Now().Add(-time.Hour)))

	s.True(s.env.IsWorkflowCompleted())
	s.Require().NoError(s.env.GetWorkflowError())
}

// Идентификатор процесса детерминирован: повторное снятие той же позиции
// перезапускает таймер, а не заводит второй процесс.
func (s *StopListWorkflowSuite) TestWorkflowIDIsDerivedFromTarget() {
	target := uuid.New()

	s.Equal("stoplist-"+target.String(), orderworkflow.StopListWorkflowID(target))
}
