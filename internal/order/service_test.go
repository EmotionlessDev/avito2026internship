package order_test

import (
	"context"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/catalog"
	"github.com/emotionlessdev/avito-kitchen/internal/order"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	userID  = uuid.MustParse("01890000-0000-7000-8000-000000000001")
	venueID = uuid.MustParse("01890000-0001-7000-8000-000000000001")
	itemID  = uuid.MustParse("01890000-0003-7000-8000-000000000101")
)

// fakeStore — хранилище заказов в памяти вместе с реальной семантикой
// идемпотентности: повтор с тем же ключом возвращает тот же заказ.
type fakeStore struct {
	byKey   map[string]order.Order
	created []order.Draft
}

func newStore() *fakeStore {
	return &fakeStore{byKey: map[string]order.Order{}}
}

func (f *fakeStore) Create(_ context.Context, draft order.Draft) (order.Order, bool, error) {
	if existing, found := f.byKey[draft.IdempotencyKey]; found {
		return existing, false, nil
	}

	created := order.Order{
		ID:               uuid.New(),
		Number:           "К-260907-0001",
		UserID:           draft.UserID,
		VenueID:          draft.VenueID,
		Status:           order.StatusCreated,
		ItemsAmountMinor: draft.ItemsAmountMinor,
		TotalAmountMinor: draft.TotalAmountMinor,
	}

	f.byKey[draft.IdempotencyKey] = created
	f.created = append(f.created, draft)

	return created, true, nil
}

func (f *fakeStore) ByIdempotencyKey(
	_ context.Context, _ uuid.UUID, key string,
) (order.Order, bool, error) {
	existing, found := f.byKey[key]

	return existing, found, nil
}

func (f *fakeStore) ByUser(_ context.Context, _, _ uuid.UUID) (order.Order, error) {
	return order.Order{}, order.ErrNotFound
}

func (f *fakeStore) ByVenue(_ context.Context, _, _ uuid.UUID) (order.Order, error) {
	return order.Order{}, order.ErrNotFound
}

func (f *fakeStore) ListByUser(
	_ context.Context, _ uuid.UUID, _ order.Filter,
) ([]order.Order, error) {
	return nil, nil
}

func (f *fakeStore) ListByVenue(
	_ context.Context, _ uuid.UUID, _ order.Filter,
) ([]order.Order, error) {
	return nil, nil
}

// fakeCarts отдаёт корзину и заранее заданный диагноз каскада.
type fakeCarts struct {
	cart    cart.Cart
	result  cart.Result
	cleared int
}

func (f *fakeCarts) Get(_ context.Context, _ uuid.UUID) (cart.Cart, error) {
	return f.cart, nil
}

func (f *fakeCarts) Validate(
	_ context.Context, _ uuid.UUID, _ *catalog.Point,
) (cart.Result, error) {
	return f.result, nil
}

func (f *fakeCarts) Clear(_ context.Context, _ uuid.UUID) error {
	f.cleared++

	return nil
}

type fakeVenues struct{}

func (fakeVenues) GetVenue(
	_ context.Context, id uuid.UUID, _ *catalog.Point,
) (catalog.VenueView, error) {
	return catalog.VenueView{Venue: catalog.Venue{
		ID: id, Name: "Хачапури и вино", Timezone: "Europe/Moscow",
	}}, nil
}

func filledCart() cart.Cart {
	id := venueID

	return cart.Cart{
		ID: uuid.New(), UserID: userID, VenueID: &id,
		Lines: []cart.Line{{
			ID: uuid.New(), MenuItemID: itemID, Name: "Хачапури по-аджарски",
			Qty: 2, UnitPriceMinor: 52000, AddedUnitPriceMinor: 52000, IsAvailable: true,
		}},
	}
}

func validResult() cart.Result {
	id := venueID

	return cart.Result{
		IsValid: true, VenueID: &id,
		ItemsAmountMinor: 104000, DeliveryFeeMinor: 14900, TotalAmountMinor: 118900,
	}
}

func request(key string) order.CreateRequest {
	return order.CreateRequest{
		IdempotencyKey: key,
		Delivery: order.Delivery{
			Address: "Москва, Большая Ордынка, 40",
			Point:   order.Point{Lat: 55.7361, Lon: 37.6252},
		},
		ContactPhone: "+79990000001",
	}
}

func TestCreateFromValidCart(t *testing.T) {
	t.Parallel()

	store := newStore()
	carts := &fakeCarts{cart: filledCart(), result: validResult()}
	service := order.NewService(store, carts, fakeVenues{})

	created, isNew, err := service.Create(t.Context(), userID, request("key-1"))

	require.NoError(t, err)
	assert.True(t, isNew)
	assert.Equal(t, order.StatusCreated, created.Status)
	assert.Equal(t, int64(118900), created.TotalAmountMinor)
	assert.Equal(t, 1, carts.cleared, "корзина очищается после оформления")

	require.Len(t, store.created, 1)
	draft := store.created[0]
	assert.Equal(t, "Москва, Большая Ордынка, 40", draft.DeliveryAddress)
	require.Len(t, draft.Items, 1)
	assert.Equal(t, 2, draft.Items[0].Qty)
	assert.Equal(t, int64(104000), draft.Items[0].TotalMinor)
}

// DoD этапа: повтор с тем же ключом идемпотентности возвращает тот же заказ,
// а не создаёт второй.
func TestCreateIsIdempotent(t *testing.T) {
	t.Parallel()

	store := newStore()
	carts := &fakeCarts{cart: filledCart(), result: validResult()}
	service := order.NewService(store, carts, fakeVenues{})

	first, isNew, err := service.Create(t.Context(), userID, request("key-1"))
	require.NoError(t, err)
	require.True(t, isNew)

	second, isNew, err := service.Create(t.Context(), userID, request("key-1"))
	require.NoError(t, err)

	assert.False(t, isNew, "повтор не создаёт заказ")
	assert.Equal(t, first.ID, second.ID)
	assert.Len(t, store.created, 1)
	assert.Equal(t, 1, carts.cleared, "повтор корзину не трогает")
}

func TestCreateRejectsBlockingIssues(t *testing.T) {
	t.Parallel()

	result := validResult()
	result.IsValid = false
	result.Issues = []cart.Issue{{Code: cart.CodeItemUnavailable, Severity: cart.SeverityBlocking}}

	store := newStore()
	carts := &fakeCarts{cart: filledCart(), result: result}
	service := order.NewService(store, carts, fakeVenues{})

	_, _, err := service.Create(t.Context(), userID, request("key-1"))

	require.ErrorIs(t, err, order.ErrCartNotValid)

	var validation order.ValidationError
	require.ErrorAs(t, err, &validation)
	assert.Equal(t, cart.CodeItemUnavailable, validation.Issues[0].Code)
	assert.Empty(t, store.created, "до базы дело не дошло")
	assert.Zero(t, carts.cleared, "корзина остаётся у пользователя")
}

// U7: подорожавшее блюдо блокирует оформление, пока пользователь не подтвердит.
func TestCreatePriceChangedNeedsConfirmation(t *testing.T) {
	t.Parallel()

	result := validResult()
	result.Issues = []cart.Issue{{Code: cart.CodePriceChanged, Severity: cart.SeverityConfirmable}}

	store := newStore()
	carts := &fakeCarts{cart: filledCart(), result: result}
	service := order.NewService(store, carts, fakeVenues{})

	_, _, err := service.Create(t.Context(), userID, request("key-1"))
	require.ErrorIs(t, err, order.ErrCartNotValid)

	confirmed := request("key-2")
	confirmed.AcceptPriceChanges = true

	_, isNew, err := service.Create(t.Context(), userID, confirmed)
	require.NoError(t, err)
	assert.True(t, isNew, "с подтверждением заказ оформляется")
}

// Флаг подтверждения не должен снимать блокирующие проблемы.
func TestCreateAcceptFlagDoesNotBypassBlocking(t *testing.T) {
	t.Parallel()

	result := validResult()
	result.IsValid = false
	result.Issues = []cart.Issue{
		{Code: cart.CodePriceChanged, Severity: cart.SeverityConfirmable},
		{Code: cart.CodeVenueClosed, Severity: cart.SeverityBlocking},
	}

	carts := &fakeCarts{cart: filledCart(), result: result}
	service := order.NewService(newStore(), carts, fakeVenues{})

	accepted := request("key-1")
	accepted.AcceptPriceChanges = true

	_, _, err := service.Create(t.Context(), userID, accepted)

	var validation order.ValidationError
	require.ErrorAs(t, err, &validation)
	require.Len(t, validation.Issues, 1)
	assert.Equal(t, cart.CodeVenueClosed, validation.Issues[0].Code)
}

func TestCreateRejectsEmptyCart(t *testing.T) {
	t.Parallel()

	carts := &fakeCarts{cart: cart.Cart{UserID: userID}, result: cart.Result{}}
	service := order.NewService(newStore(), carts, fakeVenues{})

	_, _, err := service.Create(t.Context(), userID, request("key-1"))

	require.ErrorIs(t, err, order.ErrCartNotValid)
}

// Ретрай сети приходит уже после того, как корзина очищена. Он обязан
// вернуть тот же заказ, а не ошибку «корзина пуста».
func TestCreateRepeatAfterCartCleared(t *testing.T) {
	t.Parallel()

	store := newStore()
	carts := &fakeCarts{cart: filledCart(), result: validResult()}
	service := order.NewService(store, carts, fakeVenues{})

	first, _, err := service.Create(t.Context(), userID, request("key-1"))
	require.NoError(t, err)

	// Корзина после оформления пуста — ровно то состояние, в котором
	// клиент повторяет запрос.
	carts.cart = cart.Cart{UserID: userID}
	carts.result = cart.Result{}

	second, isNew, err := service.Create(t.Context(), userID, request("key-1"))

	require.NoError(t, err)
	assert.False(t, isNew)
	assert.Equal(t, first.ID, second.ID)
}
