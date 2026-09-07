package order_test

import (
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/order"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Матрица переходов — сердце жизненного цикла заказа, поэтому она проверяется
// таблицей целиком: и разрешённые переходы, и запрещённые, и права актора.
func TestCanTransition(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		from    order.Status
		to      order.Status
		actor   order.Actor
		wantErr bool
	}{
		"оформление: заказ ушёл в заведение":    {order.StatusCreated, order.StatusPlaced, order.ActorSystem, false},
		"оформление: холд не прошёл":            {order.StatusCreated, order.StatusPaymentFailed, order.ActorSystem, false},
		"заведение приняло":                     {order.StatusPlaced, order.StatusAccepted, order.ActorVenue, false},
		"заведение отклонило":                   {order.StatusPlaced, order.StatusRejected, order.ActorVenue, false},
		"таймаут подтверждения":                 {order.StatusPlaced, order.StatusExpired, order.ActorSystem, false},
		"пользователь отменил до подтверждения": {order.StatusPlaced, order.StatusCancelled, order.ActorUser, false},
		"готовка началась по подтверждению":     {order.StatusAccepted, order.StatusCooking, order.ActorSystem, false},
		"пользователь отменил во время готовки": {order.StatusCooking, order.StatusCancelled, order.ActorUser, false},
		"заведение объявило готовность":         {order.StatusCooking, order.StatusReady, order.ActorVenue, false},
		"курьер забрал":                         {order.StatusReady, order.StatusDelivering, order.ActorSystem, false},
		"доставлено":                            {order.StatusDelivering, order.StatusCompleted, order.ActorVenue, false},

		"нельзя отменить после готовности":   {order.StatusReady, order.StatusCancelled, order.ActorUser, true},
		"нельзя отменить доставляемый заказ": {order.StatusDelivering, order.StatusCancelled, order.ActorUser, true},
		"нельзя перескочить через готовку":   {order.StatusPlaced, order.StatusReady, order.ActorVenue, true},
		"нельзя воскресить отменённый":       {order.StatusCancelled, order.StatusPlaced, order.ActorSystem, true},
		"нельзя продолжить завершённый":      {order.StatusCompleted, order.StatusDelivering, order.ActorSystem, true},
		"нельзя вернуться назад":             {order.StatusCooking, order.StatusAccepted, order.ActorSystem, true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := order.CanTransition(tt.from, tt.to, tt.actor)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
		})
	}
}

// Права актора — отдельная ошибка: «так нельзя никому» и «так нельзя тебе»
// лечатся по-разному.
func TestCanTransitionChecksActor(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		from  order.Status
		to    order.Status
		actor order.Actor
	}{
		"заведение отменяет вместо пользователя": {order.StatusPlaced, order.StatusCancelled, order.ActorVenue},
		"пользователь принимает свой заказ":      {order.StatusPlaced, order.StatusAccepted, order.ActorUser},
		"пользователь объявляет готовность":      {order.StatusCooking, order.StatusReady, order.ActorUser},
		"заведение отправляет заказ само себе":   {order.StatusCreated, order.StatusPlaced, order.ActorVenue},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := order.CanTransition(tt.from, tt.to, tt.actor)

			var forbidden order.ErrForbiddenActor
			require.ErrorAs(t, err, &forbidden, "ожидалась ошибка прав, а не запрета перехода")
			assert.Equal(t, tt.actor, forbidden.Actor)
		})
	}
}

func TestIsTerminal(t *testing.T) {
	t.Parallel()

	terminal := []order.Status{
		order.StatusCompleted, order.StatusCancelled, order.StatusRejected,
		order.StatusExpired, order.StatusPaymentFailed,
	}
	for _, status := range terminal {
		assert.True(t, order.IsTerminal(status), "%s — конечный статус", status)
	}

	alive := []order.Status{
		order.StatusCreated, order.StatusPlaced, order.StatusAccepted,
		order.StatusCooking, order.StatusReady, order.StatusDelivering,
	}
	for _, status := range alive {
		assert.False(t, order.IsTerminal(status), "%s — не конечный статус", status)
	}
}

// U11 глазами пользователя: до готовности отменить можно, после — нет.
func TestIsCancellable(t *testing.T) {
	t.Parallel()

	assert.True(t, order.IsCancellable(order.StatusPlaced))
	assert.True(t, order.IsCancellable(order.StatusAccepted))
	assert.True(t, order.IsCancellable(order.StatusCooking))

	assert.False(t, order.IsCancellable(order.StatusReady))
	assert.False(t, order.IsCancellable(order.StatusDelivering))
	assert.False(t, order.IsCancellable(order.StatusCompleted))
	assert.False(t, order.IsCancellable(order.StatusCancelled))
}
