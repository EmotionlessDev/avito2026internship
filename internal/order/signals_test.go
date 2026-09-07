package order_test

import (
	"context"
	"errors"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/order"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSignalService собирает сервис поверх хранилища с готовым заказом:
// сигнальные методы проверяют переход по текущему статусу, а не оформляют
// заказ заново.
func newSignalService(t *testing.T) (*order.Service, *fakeStore, *fakeProcess) {
	t.Helper()

	store, process := newStore(), &fakeProcess{}

	return order.NewService(store, &fakeCarts{}, fakeVenues{}, process, testLogger()), store, process
}

// Заведение подтверждает заказ сигналом, а не записью статуса: orders.status
// обязан остаться проекцией состояния процесса.
func TestAcceptSendsSignalWithoutWritingStatus(t *testing.T) {
	t.Parallel()

	service, store, process := newSignalService(t)
	stored := store.put(order.StatusPlaced)

	current, err := service.Accept(testContext(t), venueID, stored.ID, 35)
	require.NoError(t, err)

	assert.Equal(t, stored.ID, current.ID)
	assert.Equal(t, order.StatusPlaced, current.Status, "сервис статус не двигает")
	assert.Equal(t, []uuid.UUID{stored.ID}, process.accepted)
}

// Переход проверяется до сигнала: заведение обязано получить внятный отказ,
// а не «сигнал доставлен» и молчание.
func TestVenueSignalsCheckTransitionBeforeSending(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		from   order.Status
		send   func(context.Context, *order.Service, uuid.UUID) error
		sent   func(*fakeProcess) int
		allows bool
	}{
		"accept из placed": {
			from: order.StatusPlaced,
			send: func(ctx context.Context, s *order.Service, id uuid.UUID) error {
				_, err := s.Accept(ctx, venueID, id, 30)

				return err
			},
			sent:   func(p *fakeProcess) int { return len(p.accepted) },
			allows: true,
		},
		"accept из cooking": {
			from: order.StatusCooking,
			send: func(ctx context.Context, s *order.Service, id uuid.UUID) error {
				_, err := s.Accept(ctx, venueID, id, 30)

				return err
			},
			sent:   func(p *fakeProcess) int { return len(p.accepted) },
			allows: false,
		},
		"reject из placed": {
			from: order.StatusPlaced,
			send: func(ctx context.Context, s *order.Service, id uuid.UUID) error {
				_, err := s.Reject(ctx, venueID, id, "нет продуктов")

				return err
			},
			sent:   func(p *fakeProcess) int { return len(p.rejected) },
			allows: true,
		},
		"ready из cooking": {
			from: order.StatusCooking,
			send: func(ctx context.Context, s *order.Service, id uuid.UUID) error {
				_, err := s.Ready(ctx, venueID, id)

				return err
			},
			sent:   func(p *fakeProcess) int { return len(p.ready) },
			allows: true,
		},
		"ready из placed": {
			from: order.StatusPlaced,
			send: func(ctx context.Context, s *order.Service, id uuid.UUID) error {
				_, err := s.Ready(ctx, venueID, id)

				return err
			},
			sent:   func(p *fakeProcess) int { return len(p.ready) },
			allows: false,
		},
		"delivered из delivering": {
			from: order.StatusDelivering,
			send: func(ctx context.Context, s *order.Service, id uuid.UUID) error {
				_, err := s.Deliver(ctx, venueID, id)

				return err
			},
			sent:   func(p *fakeProcess) int { return len(p.delivered) },
			allows: true,
		},
		"delivered из ready": {
			from: order.StatusReady,
			send: func(ctx context.Context, s *order.Service, id uuid.UUID) error {
				_, err := s.Deliver(ctx, venueID, id)

				return err
			},
			sent:   func(p *fakeProcess) int { return len(p.delivered) },
			allows: false,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			service, store, process := newSignalService(t)
			stored := store.put(testCase.from)

			err := testCase.send(testContext(t), service, stored.ID)

			if testCase.allows {
				require.NoError(t, err)
				assert.Equal(t, 1, testCase.sent(process), "сигнал обязан уйти процессу")

				return
			}

			require.Error(t, err)
			assert.Zero(t, testCase.sent(process), "недопустимый переход до процесса не доходит")
		})
	}
}

// Чужой заказ для заведения не существует: это 404, а не 403.
func TestVenueSignalsAreScopedToVenue(t *testing.T) {
	t.Parallel()

	service, store, process := newSignalService(t)
	stored := store.put(order.StatusPlaced)

	_, err := service.Accept(testContext(t), uuid.New(), stored.ID, 30)

	require.ErrorIs(t, err, order.ErrNotFound)
	assert.Empty(t, process.accepted)
}

// Отмена проверяет право по статусу до похода в оркестратор: узнать
// «уже поздно» пользователь должен сразу (сценарий U11).
func TestCancelChecksCancellabilityBeforeSignal(t *testing.T) {
	t.Parallel()

	service, store, process := newSignalService(t)

	cancellable := store.put(order.StatusCooking)
	_, err := service.Cancel(testContext(t), userID, cancellable.ID, "передумал")
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{cancellable.ID}, process.cancelled)

	late := store.put(order.StatusReady)
	_, err = service.Cancel(testContext(t), userID, late.ID, "поздно")

	var notCancellable order.ErrNotCancellable
	require.ErrorAs(t, err, &notCancellable)
	assert.Equal(t, order.StatusReady, notCancellable.Status)
	assert.Len(t, process.cancelled, 1, "после готовности сигнал не отправляется")
}

// Чужой заказ отменить нельзя, и снова 404.
func TestCancelIsScopedToOwner(t *testing.T) {
	t.Parallel()

	service, store, _ := newSignalService(t)
	stored := store.put(order.StatusPlaced)

	_, err := service.Cancel(testContext(t), uuid.New(), stored.ID, "чужой")
	require.ErrorIs(t, err, order.ErrNotFound)
}

// Сбой оркестратора не должен выглядеть успехом: сервис возвращает ошибку.
func TestSignalFailureIsReported(t *testing.T) {
	t.Parallel()

	store, process := newStore(), &fakeProcess{err: errors.New("temporal недоступен")}
	service := order.NewService(store, &fakeCarts{}, fakeVenues{}, process, testLogger())
	stored := store.put(order.StatusPlaced)

	_, err := service.Accept(testContext(t), venueID, stored.ID, 30)
	require.Error(t, err)
}

// Чтения проксируются в хранилище как есть: сервис здесь только про права.
func TestReadsGoThroughStore(t *testing.T) {
	t.Parallel()

	service, store, _ := newSignalService(t)
	stored := store.put(order.StatusPlaced)
	store.listed = []order.Order{stored}

	found, err := service.Get(testContext(t), userID, stored.ID)
	require.NoError(t, err)
	assert.Equal(t, stored.ID, found.ID)

	_, err = service.Get(testContext(t), uuid.New(), stored.ID)
	require.ErrorIs(t, err, order.ErrNotFound)

	forVenue, err := service.GetForVenue(testContext(t), venueID, stored.ID)
	require.NoError(t, err)
	assert.Equal(t, stored.ID, forVenue.ID)

	page, err := service.List(testContext(t), userID, order.Filter{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, page, 1)

	venuePage, err := service.ListForVenue(testContext(t), venueID, order.Filter{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, venuePage, 1)
}
