package merchantapi

import (
	"context"
	"errors"
	"testing"
	"time"

	gen "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/order"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Заведение обязано отличать «заказа нет» от «так нельзя»: на первое оно
// перестанет ходить, на второе — исправит порядок вызовов.
func TestTransitionErrorsAreClassified(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err      error
		notFound bool
		conflict bool
	}{
		"чужой или несуществующий заказ": {
			err: order.ErrNotFound, notFound: true,
		},
		"недопустимый переход": {
			err: order.ErrInvalidTransition{
				From: order.StatusReady, To: order.StatusAccepted, Actor: order.ActorVenue,
			},
			conflict: true,
		},
		"переход не для этого актора": {
			err: order.ErrForbiddenActor{
				From: order.StatusPlaced, To: order.StatusCancelled, Actor: order.ActorVenue,
			},
			conflict: true,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			accept, err := acceptError(context.Background(), testCase.err)
			require.NoError(t, err)

			switch {
			case testCase.notFound:
				assert.IsType(t, gen.AcceptOrder404ApplicationProblemPlusJSONResponse{}, accept)
			case testCase.conflict:
				assert.IsType(t, gen.AcceptOrder409ApplicationProblemPlusJSONResponse{}, accept)
			}

			// Остальные три операции обязаны классифицировать так же:
			// разъехавшийся ответ на ready ломал бы автоматику заведения.
			reject, err := rejectError(context.Background(), testCase.err)
			require.NoError(t, err)
			require.NotNil(t, reject)

			ready, err := readyError(context.Background(), testCase.err)
			require.NoError(t, err)
			require.NotNil(t, ready)

			delivered, err := deliveredError(context.Background(), testCase.err)
			require.NoError(t, err)
			require.NotNil(t, delivered)
		})
	}
}

// Незнакомая ошибка уезжает наверх и становится 500: прятать баг за понятным
// кодом нельзя.
func TestTransitionErrorsPassUnknownUp(t *testing.T) {
	t.Parallel()

	unknown := errors.New("база отвалилась")

	for name, mapper := range map[string]func(context.Context, error) (any, error){
		"accept": func(ctx context.Context, err error) (any, error) {
			return acceptError(ctx, err)
		},
		"reject": func(ctx context.Context, err error) (any, error) {
			return rejectError(ctx, err)
		},
		"ready": func(ctx context.Context, err error) (any, error) {
			return readyError(ctx, err)
		},
		"delivered": func(ctx context.Context, err error) (any, error) {
			return deliveredError(ctx, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := mapper(context.Background(), unknown)
			require.ErrorIs(t, err, unknown)
		})
	}
}

// Противоречивый снапшот — это 422 с объяснением, а не 500: ошибку допустило
// заведение, и починить её может только оно.
func TestAvailabilityErrorMapping(t *testing.T) {
	t.Parallel()

	for name, cause := range map[string]error{
		"пустой запрос":           merchant.ErrEmptyAvailability,
		"авто-возврат недоступен": merchant.ErrScheduledReturnUnavailable,
	} {
		response, err := availabilityError(context.Background(), cause)
		require.NoError(t, err, name)
		assert.IsType(t, gen.SetAvailability400ApplicationProblemPlusJSONResponse{}, response, name)
	}

	_, err := availabilityError(context.Background(), errors.New("неожиданная"))
	require.Error(t, err)
}

// Счётчики импорта уходят клиенту как есть: по ним заведение понимает,
// что именно изменилось, и отличает идемпотентный повтор от работы.
func TestImportResultMapping(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	hash := "abc123"

	mapped := importResult(merchant.ImportResult{
		ImportID: id, PayloadHash: hash,
		Created: 2, Updated: 3, Archived: 1, Idempotent: false,
	})

	assert.Equal(t, id, mapped.ImportId)
	assert.Equal(t, 2, mapped.Created)
	assert.Equal(t, 3, mapped.Updated)
	assert.Equal(t, 1, mapped.Archived)
	assert.False(t, mapped.Idempotent)
	require.NotNil(t, mapped.PayloadHash)
	assert.Equal(t, hash, *mapped.PayloadHash)
}

// Настройки заведения переводятся туда и обратно без потерь: PUT с одним
// полем не должен обнулять остальные, и указатели здесь именно про это.
func TestVenueSettingsRoundTrip(t *testing.T) {
	t.Parallel()

	accepting := false
	minOrder := int64(150000)

	domainSettings := venueSettings(gen.VenueSettings{
		IsAcceptingOrders:   &accepting,
		MinOrderAmountMinor: &minOrder,
		WorkHours: &[]gen.WorkHoursInterval{
			{Weekday: 4, OpensAt: "12:00", ClosesAt: "02:00"},
		},
	})

	require.NotNil(t, domainSettings.IsAcceptingOrders)
	assert.False(t, *domainSettings.IsAcceptingOrders)
	assert.Equal(t, minOrder, *domainSettings.MinOrderAmountMinor)
	require.Len(t, domainSettings.WorkHours, 1)
	assert.Equal(t, "02:00", domainSettings.WorkHours[0].ClosesAt,
		"работа через полночь обязана доехать как есть")

	assert.Nil(t, domainSettings.DeliveryFeeMinor, "непереданное поле остаётся пустым")

	back := venueSettingsResponse(domainSettings)
	require.NotNil(t, back.IsAcceptingOrders)
	assert.False(t, *back.IsAcceptingOrders)
	require.NotNil(t, back.WorkHours)
	assert.Len(t, *back.WorkHours, 1)
}

// Заказ для заведения приезжает в его артикулах: платформенные uuid POS
// ничего не говорят.
func TestMerchantOrderMapping(t *testing.T) {
	t.Parallel()

	eta := 35
	placed := time.Now().UTC()

	mapped := merchantOrder(order.Order{
		ID: uuid.New(), Number: "К-260907-0042", Status: order.StatusAccepted,
		ItemsAmountMinor: 117000, TotalAmountMinor: 131900,
		DeliveryAddress: "Москва", ContactPhone: "+79990000001",
		ETAMinutes: &eta, PlacedAt: &placed,
		Items: []order.Item{{
			ExternalItemID: "hv-004", Name: "Хинкали с бараниной",
			Qty: 3, PriceMinor: 39000, TotalMinor: 117000,
		}},
	})

	assert.Equal(t, "К-260907-0042", mapped.Number)
	assert.Equal(t, gen.OrderStatus("accepted"), mapped.Status)
	require.Len(t, mapped.Items, 1)
	assert.Equal(t, "hv-004", mapped.Items[0].ExternalItemId)
	require.NotNil(t, mapped.EtaMinutes)
	assert.Equal(t, eta, *mapped.EtaMinutes)
}
