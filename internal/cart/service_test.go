package cart_test

import (
	"context"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/catalog"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	userID      = uuid.MustParse("01890000-0000-7000-8000-000000000001")
	otherVenue  = uuid.MustParse("01890000-0001-7000-8000-000000000002")
	groupSizeID = uuid.MustParse("01890000-0004-7000-8000-000000000001")
)

// fakeStore — корзина в памяти. Проверяем решения сервиса: что он пускает
// в корзину, что отклоняет и с какой ценой.
type fakeStore struct {
	userKnown bool
	cart      cart.Cart
	items     map[uuid.UUID]cart.MenuItem
	addErr    error

	addedItem      cart.MenuItem
	addedOptions   []uuid.UUID
	addedQty       int
	addedUnitPrice int64
	cleared        bool
}

func (f *fakeStore) UserExists(_ context.Context, _ uuid.UUID) (bool, error) {
	return f.userKnown, nil
}

func (f *fakeStore) Get(_ context.Context, _ uuid.UUID) (cart.Cart, error) {
	return f.cart, nil
}

func (f *fakeStore) MenuItemForAdd(_ context.Context, itemID uuid.UUID) (cart.MenuItem, error) {
	item, found := f.items[itemID]
	if !found {
		return cart.MenuItem{}, cart.ErrMenuItemNotFound
	}

	return item, nil
}

func (f *fakeStore) AddLine(
	_ context.Context, _ uuid.UUID, item cart.MenuItem,
	optionIDs []uuid.UUID, qty int, unitPrice int64,
) error {
	if f.addErr != nil {
		return f.addErr
	}

	f.addedItem = item
	f.addedOptions = optionIDs
	f.addedQty = qty
	f.addedUnitPrice = unitPrice

	return nil
}

func (f *fakeStore) UpdateQty(_ context.Context, _, _ uuid.UUID, _ int) error { return nil }
func (f *fakeStore) DeleteLine(_ context.Context, _, _ uuid.UUID) error       { return nil }

func (f *fakeStore) Clear(_ context.Context, _ uuid.UUID) error {
	f.cleared = true

	return nil
}

// stubVenues отдаёт заведение, каким его видит каталог.
type stubVenues struct {
	view catalog.VenueView
	err  error
}

func (s stubVenues) GetVenue(
	_ context.Context, _ uuid.UUID, _ *catalog.Point,
) (catalog.VenueView, error) {
	return s.view, s.err
}

func hachapuri() cart.MenuItem {
	return cart.MenuItem{
		ID:          itemID(1),
		VenueID:     venueID,
		Name:        "Хачапури по-аджарски",
		PriceMinor:  52000,
		IsAvailable: true,
		Groups: []cart.OptionGroup{{
			ID: groupSizeID, Name: "Размер", MinChoices: 1, MaxChoices: 1,
			Options: map[uuid.UUID]int64{
				optionID(1): 0,     // обычная
				optionID(2): 15000, // большая
			},
		}},
	}
}

func newService(store *fakeStore) *cart.Service {
	return cart.NewService(store, stubVenues{view: catalog.VenueView{
		Venue:     catalog.Venue{ID: venueID, IsAcceptingOrders: true, MinOrderAmountMinor: 10000},
		IsOpenNow: true,
	}})
}

func TestEnsureUser(t *testing.T) {
	t.Parallel()

	t.Run("известный пользователь", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, newService(&fakeStore{userKnown: true}).EnsureUser(t.Context(), userID))
	})

	t.Run("неизвестный пользователь", func(t *testing.T) {
		t.Parallel()

		err := newService(&fakeStore{userKnown: false}).EnsureUser(t.Context(), userID)
		require.ErrorIs(t, err, cart.ErrUnknownUser)
	})
}

func TestAddItemCountsOptionsInPrice(t *testing.T) {
	t.Parallel()

	store := &fakeStore{items: map[uuid.UUID]cart.MenuItem{itemID(1): hachapuri()}}

	_, err := newService(store).AddItem(t.Context(), userID, cart.AddRequest{
		ItemID:    itemID(1),
		Qty:       2,
		OptionIDs: []uuid.UUID{optionID(2)},
	})

	require.NoError(t, err)
	assert.Equal(t, int64(52000+15000), store.addedUnitPrice, "надбавка опции входит в цену за штуку")
	assert.Equal(t, 2, store.addedQty)
}

func TestAddItemRejectsBadOptions(t *testing.T) {
	t.Parallel()

	item := hachapuri()

	tests := map[string][]uuid.UUID{
		"обязательная группа пропущена": {},
		"выбрано больше, чем можно":     {optionID(1), optionID(2)},
		"чужой модификатор":             {optionID(9)},
		"один и тот же дважды":          {optionID(1), optionID(1)},
	}

	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := &fakeStore{items: map[uuid.UUID]cart.MenuItem{itemID(1): item}}

			_, err := newService(store).AddItem(t.Context(), userID, cart.AddRequest{
				ItemID: itemID(1), Qty: 1, OptionIDs: options,
			})

			require.ErrorIs(t, err, cart.ErrInvalidOptions)
			assert.Zero(t, store.addedQty, "в корзину ничего не ушло")
		})
	}
}

func TestAddItemRejectsUnavailable(t *testing.T) {
	t.Parallel()

	item := hachapuri()
	item.IsAvailable = false
	item.Groups = nil

	store := &fakeStore{items: map[uuid.UUID]cart.MenuItem{itemID(1): item}}

	_, err := newService(store).AddItem(t.Context(), userID, cart.AddRequest{ItemID: itemID(1), Qty: 1})

	require.ErrorIs(t, err, cart.ErrMenuItemNotFound)
	assert.Zero(t, store.addedQty)
}

func TestAddItemUnknownItem(t *testing.T) {
	t.Parallel()

	store := &fakeStore{items: map[uuid.UUID]cart.MenuItem{}}

	_, err := newService(store).AddItem(t.Context(), userID, cart.AddRequest{ItemID: itemID(9), Qty: 1})

	require.ErrorIs(t, err, cart.ErrMenuItemNotFound)
}

// U8: блюдо другого заведения не попадает в корзину молча.
func TestAddItemFromAnotherVenue(t *testing.T) {
	t.Parallel()

	item := hachapuri()
	item.VenueID = otherVenue
	item.Groups = nil

	store := &fakeStore{
		items:  map[uuid.UUID]cart.MenuItem{itemID(1): item},
		addErr: cart.ErrMixedVenues,
	}

	_, err := newService(store).AddItem(t.Context(), userID, cart.AddRequest{ItemID: itemID(1), Qty: 1})

	require.ErrorIs(t, err, cart.ErrMixedVenues)
}

func TestValidateEmptyCartSkipsVenueLookup(t *testing.T) {
	t.Parallel()

	// Каталог намеренно сломан: пустая корзина не должна его дёргать.
	service := cart.NewService(&fakeStore{}, stubVenues{err: catalog.ErrVenueNotFound})

	result, err := service.Validate(t.Context(), userID, nil)

	require.NoError(t, err)
	assert.False(t, result.IsValid)
	assert.Empty(t, result.Issues)
}

func TestValidateUsesVenueState(t *testing.T) {
	t.Parallel()

	id := venueID
	store := &fakeStore{cart: cart.Cart{
		ID: uuid.New(), UserID: userID, VenueID: &id,
		Lines: []cart.Line{{
			MenuItemID: itemID(1), Name: "Хачапури по-аджарски", Qty: 1,
			AddedUnitPriceMinor: 52000, UnitPriceMinor: 52000, IsAvailable: true,
		}},
	}}

	service := cart.NewService(store, stubVenues{view: catalog.VenueView{
		Venue: catalog.Venue{
			ID: venueID, IsAcceptingOrders: false, MinOrderAmountMinor: 10000,
		},
		IsOpenNow: true,
	}})

	result, err := service.Validate(t.Context(), userID, nil)

	require.NoError(t, err)
	assert.Equal(t, []string{cart.CodeVenueNotAccepting}, codes(result.Issues),
		"состояние заведения приезжает из каталога")
}

func TestClear(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	require.NoError(t, newService(store).Clear(t.Context(), userID))
	assert.True(t, store.cleared)
}
