package order_test

import (
	"context"
	"errors"
	"log/slog"
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
	// existing — заказы, которые «уже лежат» в базе: их отдают чтения
	// и сигнальные методы.
	existing map[uuid.UUID]order.Order
	listed   []order.Order
	listErr  error
}

func newStore() *fakeStore {
	return &fakeStore{byKey: map[string]order.Order{}, existing: map[uuid.UUID]order.Order{}}
}

// put кладёт заказ так, будто он уже оформлен: тестам сигналов нужен
// заказ в конкретном статусе, а не путь его оформления.
func (f *fakeStore) put(status order.Status) order.Order {
	stored := order.Order{
		ID: uuid.New(), Number: "К-260907-0042",
		UserID: userID, VenueID: venueID, Status: status,
	}
	f.existing[stored.ID] = stored

	return stored
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
		Fingerprint:      draft.Fingerprint,
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

func (f *fakeStore) ByUser(_ context.Context, owner, orderID uuid.UUID) (order.Order, error) {
	stored, found := f.existing[orderID]
	if !found || stored.UserID != owner {
		return order.Order{}, order.ErrNotFound
	}

	return stored, nil
}

func (f *fakeStore) ByVenue(_ context.Context, venue, orderID uuid.UUID) (order.Order, error) {
	stored, found := f.existing[orderID]
	if !found || stored.VenueID != venue {
		return order.Order{}, order.ErrNotFound
	}

	return stored, nil
}

func (f *fakeStore) ListByUser(
	_ context.Context, _ uuid.UUID, _ order.Filter,
) ([]order.Order, error) {
	return f.listed, f.listErr
}

func (f *fakeStore) ListByVenue(
	_ context.Context, _ uuid.UUID, _ order.Filter,
) ([]order.Order, error) {
	return f.listed, f.listErr
}

// fakeProcess запоминает, что сервис попросил у процесса заказа.
// Сам процесс проверяется отдельно, в тестах workflow.
type fakeProcess struct {
	started   []order.ProcessInput
	cancelled []uuid.UUID
	accepted  []uuid.UUID
	rejected  []uuid.UUID
	ready     []uuid.UUID
	delivered []uuid.UUID
	err       error
}

func (f *fakeProcess) Start(_ context.Context, input order.ProcessInput) error {
	if f.err != nil {
		return f.err
	}

	f.started = append(f.started, input)

	return nil
}

func (f *fakeProcess) VenueAccepted(_ context.Context, orderID uuid.UUID, _ int) error {
	f.accepted = append(f.accepted, orderID)

	return f.err
}

func (f *fakeProcess) VenueRejected(_ context.Context, orderID uuid.UUID, _ string) error {
	f.rejected = append(f.rejected, orderID)

	return f.err
}

func (f *fakeProcess) Ready(_ context.Context, orderID uuid.UUID) error {
	f.ready = append(f.ready, orderID)

	return f.err
}

func (f *fakeProcess) Delivered(_ context.Context, orderID uuid.UUID) error {
	f.delivered = append(f.delivered, orderID)

	return f.err
}

func (f *fakeProcess) Cancel(_ context.Context, orderID uuid.UUID, _ string) error {
	f.cancelled = append(f.cancelled, orderID)

	return f.err
}

// testLogger гасит вывод: сервис пишет предупреждение, когда процесс
// не стартовал, и в тестах этот шум не нужен.
func testLogger() *slog.Logger {
	return slog.New(discardHandler())
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
	service := order.NewService(store, carts, fakeVenues{}, &fakeProcess{}, testLogger())

	created, isNew, err := service.Create(testContext(t), userID, request("key-1"))

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
	service := order.NewService(store, carts, fakeVenues{}, &fakeProcess{}, testLogger())

	first, isNew, err := service.Create(testContext(t), userID, request("key-1"))
	require.NoError(t, err)
	require.True(t, isNew)

	second, isNew, err := service.Create(testContext(t), userID, request("key-1"))
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
	service := order.NewService(store, carts, fakeVenues{}, &fakeProcess{}, testLogger())

	_, _, err := service.Create(testContext(t), userID, request("key-1"))

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
	service := order.NewService(store, carts, fakeVenues{}, &fakeProcess{}, testLogger())

	_, _, err := service.Create(testContext(t), userID, request("key-1"))
	require.ErrorIs(t, err, order.ErrCartNotValid)

	confirmed := request("key-2")
	confirmed.AcceptPriceChanges = true

	_, isNew, err := service.Create(testContext(t), userID, confirmed)
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
	service := order.NewService(newStore(), carts, fakeVenues{}, &fakeProcess{}, testLogger())

	accepted := request("key-1")
	accepted.AcceptPriceChanges = true

	_, _, err := service.Create(testContext(t), userID, accepted)

	var validation order.ValidationError
	require.ErrorAs(t, err, &validation)
	require.Len(t, validation.Issues, 1)
	assert.Equal(t, cart.CodeVenueClosed, validation.Issues[0].Code)
}

func TestCreateRejectsEmptyCart(t *testing.T) {
	t.Parallel()

	carts := &fakeCarts{cart: cart.Cart{UserID: userID}, result: cart.Result{}}
	service := order.NewService(newStore(), carts, fakeVenues{}, &fakeProcess{}, testLogger())

	_, _, err := service.Create(testContext(t), userID, request("key-1"))

	// Пустая корзина — не «нашлись проблемы», а «нечего оформлять»:
	// клиенту нужен собственный код, а не CART_NOT_VALID с пустым issues[].
	require.ErrorIs(t, err, order.ErrCartEmpty)

	var validation order.ValidationError
	require.NotErrorAs(t, err, &validation)
}

// Ретрай сети приходит уже после того, как корзина очищена. Он обязан
// вернуть тот же заказ, а не ошибку «корзина пуста».
func TestCreateRepeatAfterCartCleared(t *testing.T) {
	t.Parallel()

	store := newStore()
	carts := &fakeCarts{cart: filledCart(), result: validResult()}
	service := order.NewService(store, carts, fakeVenues{}, &fakeProcess{}, testLogger())

	first, _, err := service.Create(testContext(t), userID, request("key-1"))
	require.NoError(t, err)

	// Корзина после оформления пуста — ровно то состояние, в котором
	// клиент повторяет запрос.
	carts.cart = cart.Cart{UserID: userID}
	carts.result = cart.Result{}

	second, isNew, err := service.Create(testContext(t), userID, request("key-1"))

	require.NoError(t, err)
	assert.False(t, isNew)
	assert.Equal(t, first.ID, second.ID)
}

// Тот же ключ с другим телом — ошибка клиента: старый заказ вернуть нельзя,
// иначе новый адрес доставки тихо потеряется.
func TestCreateRejectsSameKeyWithDifferentBody(t *testing.T) {
	t.Parallel()

	store := newStore()
	carts := &fakeCarts{cart: filledCart(), result: validResult()}
	service := order.NewService(store, carts, fakeVenues{}, &fakeProcess{}, testLogger())

	_, _, err := service.Create(testContext(t), userID, request("key-1"))
	require.NoError(t, err)

	other := request("key-1")
	other.Delivery.Address = "Москва, другой адрес, 1"

	_, _, err = service.Create(testContext(t), userID, other)

	require.ErrorIs(t, err, order.ErrIdempotencyConflict)
	assert.Len(t, store.created, 1, "второй заказ не создан")
}

func TestFingerprintDependsOnRequestFields(t *testing.T) {
	t.Parallel()

	base := request("key-1")

	other := base
	other.ContactPhone = "+79990000002"

	sameKeyOtherRequest := base
	sameKeyOtherRequest.IdempotencyKey = "key-2"

	assert.NotEqual(t, order.Fingerprint(base), order.Fingerprint(other),
		"телефон входит в отпечаток")
	assert.Equal(t, order.Fingerprint(base), order.Fingerprint(sameKeyOtherRequest),
		"сам ключ в отпечаток не входит: он и так проверяется отдельно")
}

// Процесс заказа запускается сразу после оформления — это быстрый путь;
// страховкой служит потребитель событий.
func TestCreateStartsProcess(t *testing.T) {
	t.Parallel()

	process := &fakeProcess{}
	carts := &fakeCarts{cart: filledCart(), result: validResult()}
	service := order.NewService(newStore(), carts, fakeVenues{}, process, testLogger())

	created, _, err := service.Create(testContext(t), userID, request("key-1"))
	require.NoError(t, err)

	require.Len(t, process.started, 1)
	assert.Equal(t, created.ID, process.started[0].OrderID)
	assert.Equal(t, created.TotalAmountMinor, process.started[0].TotalAmountMinor)
}

// Заказ уже создан, поэтому неудачный запуск процесса не должен превращаться
// в ошибку для клиента: процесс поднимет страховочный потребитель.
func TestCreateSurvivesProcessFailure(t *testing.T) {
	t.Parallel()

	process := &fakeProcess{err: errProcessUnavailable}
	carts := &fakeCarts{cart: filledCart(), result: validResult()}
	service := order.NewService(newStore(), carts, fakeVenues{}, process, testLogger())

	created, isNew, err := service.Create(testContext(t), userID, request("key-1"))

	require.NoError(t, err)
	assert.True(t, isNew)
	assert.NotEmpty(t, created.Number)
}

var errProcessUnavailable = errors.New("оркестратор недоступен")
