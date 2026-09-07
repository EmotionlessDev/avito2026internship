package workflow

import (
	"context"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/merchant"

	"github.com/google/uuid"
	sdk "go.temporal.io/sdk/workflow"
)

// SignalStopListCancel — заведение вернуло позицию раньше срока.
const SignalStopListCancel = "stoplist_cancel"

// StopListInput — что и до какого момента снято с продажи.
type StopListInput struct {
	VenueID  uuid.UUID               `json:"venue_id"`
	Kind     merchant.StopTargetKind `json:"kind"`
	TargetID uuid.UUID               `json:"target_id"`
	Until    time.Time               `json:"until"`
}

// StopListWorkflow возвращает позицию в продажу по таймеру.
//
// Двадцать строк, которые объясняют, зачем нужен durable execution лучше
// длинного текста: заведение снимает блюдо «до конца дня», процесс спит
// столько, сколько нужно, переживает деплой и просыпается ровно в срок
// (docs/DESIGN.md §6.3).
//
// Идентификатор workflow детерминирован (stoplist-<target>), поэтому повторное
// снятие той же позиции перезапускает таймер, а не заводит второй.
func StopListWorkflow(ctx sdk.Context, input StopListInput) error {
	ctx = sdk.WithActivityOptions(ctx, activityOptions())
	log := sdk.GetLogger(ctx)

	delay := input.Until.Sub(sdk.Now(ctx))
	if delay <= 0 {
		// Момент возврата уже прошёл — возвращаем сразу, а не отказываем:
		// заведение просило вернуть «к этому времени», и оно наступило.
		return returnToSale(ctx, input)
	}

	timer, cancelTimer := sdk.WithCancel(ctx)
	defer cancelTimer()

	returned := false
	selector := sdk.NewSelector(ctx)

	selector.AddFuture(sdk.NewTimer(timer, delay), func(sdk.Future) {
		returned = true
	})

	selector.AddReceive(sdk.GetSignalChannel(ctx, SignalStopListCancel), func(c sdk.ReceiveChannel, _ bool) {
		c.Receive(ctx, nil)

		log.Info("возврат в продажу отменён: заведение вернуло позицию раньше срока")
	})

	selector.Select(ctx)

	if !returned {
		return nil
	}

	return returnToSale(ctx, input)
}

func returnToSale(ctx sdk.Context, input StopListInput) error {
	var activities *StopListActivities

	return sdk.ExecuteActivity(ctx, activities.ReturnToSale, input).Get(ctx, nil)
}

// StopListActivities — сторона стоп-листа, работающая с базой.
type StopListActivities struct {
	menu *merchant.Repository
}

// NewStopListActivities собирает активности стоп-листа.
func NewStopListActivities(menu *merchant.Repository) *StopListActivities {
	return &StopListActivities{menu: menu}
}

// ReturnToSale возвращает позицию или опцию в продажу.
//
// Активность идемпотентна: повторный вызов на уже доступной позиции ничего
// не меняет и не считается ошибкой.
func (a *StopListActivities) ReturnToSale(ctx context.Context, input StopListInput) error {
	return a.menu.ReturnToSale(ctx, input.VenueID, merchant.StopTarget{
		Kind: input.Kind,
		ID:   input.TargetID,
	})
}

// StopListWorkflowID — детерминированный идентификатор процесса стоп-листа.
func StopListWorkflowID(target uuid.UUID) string {
	return "stoplist-" + target.String()
}
