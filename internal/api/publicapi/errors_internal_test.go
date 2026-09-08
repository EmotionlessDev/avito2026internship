package publicapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/order"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Перевод доменной ошибки в problem+json — тонкое место контура: клиент
// разбирает ответ по коду, а не по тексту, и разъехавшийся код молча ломает
// клиентскую ветку.
func TestCreateOrderErrorMapping(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err     error
		status  int
		code    string
		typeURI string
		issues  int
	}{
		"конфликт ключа идемпотентности": {
			err:     order.ErrIdempotencyConflict,
			status:  http.StatusConflict,
			code:    "IDEMPOTENCY_KEY_CONFLICT",
			typeURI: "https://kitchen.avito.ru/errors/idempotency-key-conflict",
		},
		"пустая корзина": {
			err:     order.ErrCartEmpty,
			status:  http.StatusUnprocessableEntity,
			code:    "CART_EMPTY",
			typeURI: "https://kitchen.avito.ru/errors/cart-empty",
		},
		"блокирующие проблемы корзины": {
			err: order.ValidationError{Issues: []cart.Issue{
				{Code: cart.CodeItemUnavailable, Severity: cart.SeverityBlocking},
				{Code: cart.CodeVenueClosed, Severity: cart.SeverityBlocking},
			}},
			status:  http.StatusUnprocessableEntity,
			code:    "CART_NOT_VALID",
			typeURI: "https://kitchen.avito.ru/errors/cart-not-valid",
			issues:  2,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			response, err := createOrderError(context.Background(), testCase.err)
			require.NoError(t, err, "известная ошибка не должна доезжать до 500")

			body := orderProblemBody(t, response)
			assert.Equal(t, testCase.status, body.Status)
			assert.Equal(t, testCase.code, body.Code)
			assert.Equal(t, testCase.typeURI, body.Type,
				"type выводится из кода — клиент ходит по нему за документацией")
			assert.Len(t, body.Issues, testCase.issues)
		})
	}
}

// Незнакомая ошибка не превращается в вежливый 422: она обязана уехать
// наверх и стать 500, иначе баг спрячется за понятным кодом.
func TestCreateOrderErrorPassesUnknownUp(t *testing.T) {
	t.Parallel()

	response, err := createOrderError(context.Background(), assert.AnError)

	require.Error(t, err)
	assert.Nil(t, response)
}

// Отказ в отмене объясняется по текущему статусу: один текст на все
// статусы врал бы про «блюда уже приготовлены» заказу в created.
func TestNotCancellableDetailDependsOnStatus(t *testing.T) {
	t.Parallel()

	cases := map[order.Status]string{
		order.StatusCreated:       "ещё оформляется",
		order.StatusReady:         "уже приготовило",
		order.StatusDelivering:    "уже приготовило",
		order.StatusCompleted:     "уже завершён",
		order.StatusCancelled:     "уже завершён",
		order.StatusPaymentFailed: "уже завершён",
		order.StatusPlaced:        "отмена больше не проходит",
	}

	for status, expected := range cases {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			assert.Contains(t, notCancellableDetail(status), expected)
		})
	}
}

// Каскад отдаёт клиенту все поля проблемы, а не только код: без old/new цены
// диалог «цена выросла, продолжить?» нарисовать нечем.
func TestCartIssueCarriesDetails(t *testing.T) {
	t.Parallel()

	itemID, optionID := uuid.New(), uuid.New()
	old, current := int64(45000), int64(52000)
	minOrder, missing := int64(100000), int64(22000)

	price := cartIssue(cart.Issue{
		Code: cart.CodePriceChanged, Severity: cart.SeverityConfirmable,
		ItemID: &itemID, OldPriceMinor: &old, NewPriceMinor: &current,
	})

	assert.Equal(t, "PRICE_CHANGED", string(price.Code))
	assert.Equal(t, "confirmable", string(price.Severity))
	require.NotNil(t, price.OldPriceMinor)
	assert.Equal(t, old, *price.OldPriceMinor)
	assert.Equal(t, current, *price.NewPriceMinor)

	minimum := cartIssue(cart.Issue{
		Code: cart.CodeMinOrderNotMet, Severity: cart.SeverityBlocking,
		MinOrderMinor: &minOrder, MissingMinor: &missing,
	})

	require.NotNil(t, minimum.MissingMinor)
	assert.Equal(t, missing, *minimum.MissingMinor)

	option := cartIssue(cart.Issue{
		Code: cart.CodeOptionUnavailable, Severity: cart.SeverityBlocking,
		OptionID: &optionID, OptionName: "Большая",
	})

	require.NotNil(t, option.OptionId)
	assert.Equal(t, optionID, *option.OptionId)
	require.NotNil(t, option.OptionName)
	assert.Equal(t, "Большая", *option.OptionName)
}

// Пустой курсор — это null в ответе, а не пустая строка: клиент отличает
// «страниц больше нет» от «вот пустой курсор».
func TestNextCursorIsNilWhenEmpty(t *testing.T) {
	t.Parallel()

	assert.Nil(t, nextCursor(""))
	require.NotNil(t, nextCursor("abc"))
	assert.Equal(t, "abc", *nextCursor("abc"))
}

// orderProblemBody приводит любой из ответов ошибки оформления к общей форме:
// у каждого свой сгенерированный тип, а проверять нужно одни и те же поля.
func orderProblemBody(t *testing.T, response public.CreateOrderResponseObject) struct {
	Status   int
	Code     string
	Type     string
	Instance *string
	Issues   []public.CartIssue
} {
	t.Helper()

	switch typed := response.(type) {
	case public.CreateOrder409ApplicationProblemPlusJSONResponse:
		return struct {
			Status   int
			Code     string
			Type     string
			Instance *string
			Issues   []public.CartIssue
		}{typed.Status, typed.Code, typed.Type, typed.Instance, nil}
	case public.CreateOrder422ApplicationProblemPlusJSONResponse:
		return struct {
			Status   int
			Code     string
			Type     string
			Instance *string
			Issues   []public.CartIssue
		}{typed.Status, typed.Code, typed.Type, typed.Instance, typed.Issues}
	default:
		t.Fatalf("неожиданный тип ответа: %T", response)

		return struct {
			Status   int
			Code     string
			Type     string
			Instance *string
			Issues   []public.CartIssue
		}{}
	}
}
