package cart_test

import (
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/catalog"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func itemID(n int) uuid.UUID {
	return uuid.MustParse("01890000-0003-7000-8000-00000000000" + string(rune('0'+n)))
}

func optionID(n int) uuid.UUID {
	return uuid.MustParse("01890000-0005-7000-8000-00000000000" + string(rune('0'+n)))
}

var venueID = uuid.MustParse("01890000-0001-7000-8000-000000000001")

// venueOK — заведение, у которого всё в порядке: открыто, принимает заказы,
// минимум маленький. Каждый тест ломает ровно одну вещь.
func venueOK() cart.Venue {
	return cart.Venue{
		ID:                  venueID,
		IsOpenNow:           true,
		IsAcceptingOrders:   true,
		Point:               catalog.Point{Lat: 55.7402, Lon: 37.6280},
		DeliveryRadiusM:     5000,
		MinOrderAmountMinor: 10000,
		DeliveryFeeMinor:    14900,
	}
}

// lineOK — позиция, с которой всё хорошо: доступна, цена не менялась.
func lineOK() cart.Line {
	return cart.Line{
		ID:                  uuid.New(),
		MenuItemID:          itemID(1),
		Name:                "Хачапури по-аджарски",
		Qty:                 1,
		AddedUnitPriceMinor: 52000,
		UnitPriceMinor:      52000,
		IsAvailable:         true,
	}
}

func cartWith(lines ...cart.Line) cart.Cart {
	id := venueID

	return cart.Cart{
		ID:      uuid.New(),
		UserID:  uuid.MustParse("01890000-0000-7000-8000-000000000001"),
		VenueID: &id,
		Lines:   lines,
	}
}

func codes(issues []cart.Issue) []string {
	out := make([]string, 0, len(issues))
	for i := range issues {
		out = append(out, issues[i].Code)
	}

	return out
}

func TestValidateHappyPath(t *testing.T) {
	t.Parallel()

	result := cart.Validate(cart.ValidationInput{
		Venue: venueOK(),
		Cart:  cartWith(lineOK()),
	})

	assert.True(t, result.IsValid)
	assert.Empty(t, result.Issues)
	assert.Equal(t, int64(52000), result.ItemsAmountMinor)
	assert.Zero(t, result.DeliveryFeeMinor, "без адреса доставку не считаем")
}

// Пустая корзина: оформлять нечего, но и проблемы нет — так договорились
// в контракте.
func TestValidateEmptyCart(t *testing.T) {
	t.Parallel()

	result := cart.Validate(cart.ValidationInput{Venue: venueOK(), Cart: cart.Cart{}})

	assert.False(t, result.IsValid)
	assert.Empty(t, result.Issues)
	assert.Zero(t, result.TotalAmountMinor)
}

// U2: товар кончился, пока лежал в корзине.
func TestValidateItemUnavailable(t *testing.T) {
	t.Parallel()

	line := lineOK()
	line.IsAvailable = false

	result := cart.Validate(cart.ValidationInput{Venue: venueOK(), Cart: cartWith(line)})

	require.Equal(t, []string{cart.CodeItemUnavailable}, codes(result.Issues))
	assert.False(t, result.IsValid)
	assert.Equal(t, cart.SeverityBlocking, result.Issues[0].Severity)
	require.NotNil(t, result.Issues[0].ItemID)
	assert.Equal(t, itemID(1), *result.Issues[0].ItemID)
	assert.Equal(t, "Хачапури по-аджарски", result.Issues[0].ItemName)
}

// U13: сняли модификатор, а не всё блюдо.
func TestValidateOptionUnavailable(t *testing.T) {
	t.Parallel()

	line := lineOK()
	line.Options = []cart.LineOption{
		{OptionID: optionID(1), Name: "Двойной сыр", PriceDeltaMinor: 9000, IsAvailable: false},
		{OptionID: optionID(2), Name: "Яйцо", PriceDeltaMinor: 5000, IsAvailable: true},
	}

	result := cart.Validate(cart.ValidationInput{Venue: venueOK(), Cart: cartWith(line)})

	require.Equal(t, []string{cart.CodeOptionUnavailable}, codes(result.Issues))
	assert.False(t, result.IsValid)
	require.NotNil(t, result.Issues[0].OptionID)
	assert.Equal(t, optionID(1), *result.Issues[0].OptionID)
	assert.Equal(t, "Двойной сыр", result.Issues[0].OptionName)
	assert.Equal(t, itemID(1), *result.Issues[0].ItemID, "проблема опции указывает и на позицию")
}

// U7: цена выросла — это подтверждаемая проблема, а не блокирующая.
func TestValidatePriceChanged(t *testing.T) {
	t.Parallel()

	line := lineOK()
	line.AddedUnitPriceMinor = 45000
	line.UnitPriceMinor = 52000

	result := cart.Validate(cart.ValidationInput{Venue: venueOK(), Cart: cartWith(line)})

	require.Equal(t, []string{cart.CodePriceChanged}, codes(result.Issues))
	assert.Equal(t, cart.SeverityConfirmable, result.Issues[0].Severity)
	assert.True(t, result.IsValid, "подтверждаемая проблема не гасит кнопку")
	assert.Equal(t, int64(45000), *result.Issues[0].OldPriceMinor)
	assert.Equal(t, int64(52000), *result.Issues[0].NewPriceMinor)
	assert.Equal(t, int64(52000), result.ItemsAmountMinor, "сумма считается по новой цене")
}

func TestValidatePriceDropIsAlsoReported(t *testing.T) {
	t.Parallel()

	line := lineOK()
	line.AddedUnitPriceMinor = 52000
	line.UnitPriceMinor = 45000

	result := cart.Validate(cart.ValidationInput{Venue: venueOK(), Cart: cartWith(line)})

	require.Equal(t, []string{cart.CodePriceChanged}, codes(result.Issues),
		"подешевевшее блюдо тоже требует подтверждения: чек изменится")
}

// U3: заведение закрыто — вместе с обещанием, когда откроется.
func TestValidateVenueClosed(t *testing.T) {
	t.Parallel()

	nextOpen := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	venue := venueOK()
	venue.IsOpenNow = false
	venue.NextOpenAt = &nextOpen

	result := cart.Validate(cart.ValidationInput{Venue: venue, Cart: cartWith(lineOK())})

	require.Equal(t, []string{cart.CodeVenueClosed}, codes(result.Issues))
	assert.False(t, result.IsValid)
	require.NotNil(t, result.Issues[0].NextOpenAt)
	assert.Equal(t, nextOpen, *result.Issues[0].NextOpenAt)
}

// U4: заведение работает, но кухня перегружена.
func TestValidateVenueNotAccepting(t *testing.T) {
	t.Parallel()

	venue := venueOK()
	venue.IsAcceptingOrders = false

	result := cart.Validate(cart.ValidationInput{Venue: venue, Cart: cartWith(lineOK())})

	require.Equal(t, []string{cart.CodeVenueNotAccepting}, codes(result.Issues))
	assert.False(t, result.IsValid)
}

// U5: адрес вне радиуса доставки.
func TestValidateOutOfDeliveryZone(t *testing.T) {
	t.Parallel()

	venue := venueOK()
	venue.DeliveryRadiusM = 1000

	// Точка «Супры» из сида — 5.27 км от «Хачапури и вино».
	far := catalog.Point{Lat: 55.7048, Lon: 37.5720}

	result := cart.Validate(cart.ValidationInput{
		Venue:    venue,
		Cart:     cartWith(lineOK()),
		Delivery: &far,
	})

	require.Equal(t, []string{cart.CodeOutOfDeliveryZone}, codes(result.Issues))
	assert.False(t, result.IsValid)
	require.NotNil(t, result.Issues[0].DistanceM)
	assert.Greater(t, *result.Issues[0].DistanceM, 1000)
	assert.Equal(t, 1000, *result.Issues[0].DeliveryRadiusM)
	assert.Equal(t, int64(14900), result.DeliveryFeeMinor, "с адресом доставка попадает в итог")
}

func TestValidateInsideDeliveryZone(t *testing.T) {
	t.Parallel()

	near := catalog.Point{Lat: 55.7361, Lon: 37.6252}

	result := cart.Validate(cart.ValidationInput{
		Venue:    venueOK(),
		Cart:     cartWith(lineOK()),
		Delivery: &near,
	})

	assert.True(t, result.IsValid)
	assert.Empty(t, result.Issues)
	assert.Equal(t, int64(52000+14900), result.TotalAmountMinor)
}

// U6: не набран минимум. Минимум сравнивается со стоимостью блюд —
// доставка не помогает его набрать.
func TestValidateMinOrderNotMet(t *testing.T) {
	t.Parallel()

	venue := venueOK()
	venue.MinOrderAmountMinor = 100000

	line := lineOK()
	line.UnitPriceMinor = 78000
	line.AddedUnitPriceMinor = 78000

	near := catalog.Point{Lat: 55.7361, Lon: 37.6252}

	result := cart.Validate(cart.ValidationInput{
		Venue:    venue,
		Cart:     cartWith(line),
		Delivery: &near,
	})

	require.Equal(t, []string{cart.CodeMinOrderNotMet}, codes(result.Issues))
	assert.False(t, result.IsValid)
	assert.Equal(t, int64(100000), *result.Issues[0].MinOrderMinor)
	assert.Equal(t, int64(22000), *result.Issues[0].MissingMinor,
		"доставка не засчитывается в минимум")
}

func TestValidateMinOrderExactlyMet(t *testing.T) {
	t.Parallel()

	venue := venueOK()
	venue.MinOrderAmountMinor = 52000

	result := cart.Validate(cart.ValidationInput{Venue: venue, Cart: cartWith(lineOK())})

	assert.True(t, result.IsValid, "ровно минимум — уже достаточно")
	assert.Empty(t, result.Issues)
}

// Каскад не останавливается на первой проблеме: пользователь должен увидеть
// сразу весь список, иначе он будет чинить корзину по одной ошибке за запрос.
func TestValidateCollectsAllProblems(t *testing.T) {
	t.Parallel()

	venue := venueOK()
	venue.IsOpenNow = false
	venue.IsAcceptingOrders = false
	venue.MinOrderAmountMinor = 100000
	venue.DeliveryRadiusM = 100

	unavailable := lineOK()
	unavailable.IsAvailable = false
	unavailable.Options = []cart.LineOption{
		{OptionID: optionID(1), Name: "Двойной сыр", IsAvailable: false},
	}

	pricier := lineOK()
	pricier.MenuItemID = itemID(2)
	pricier.AddedUnitPriceMinor = 30000
	pricier.UnitPriceMinor = 36000

	far := catalog.Point{Lat: 55.7048, Lon: 37.5720}

	result := cart.Validate(cart.ValidationInput{
		Venue:    venue,
		Cart:     cartWith(unavailable, pricier),
		Delivery: &far,
	})

	assert.ElementsMatch(t, []string{
		cart.CodeVenueClosed,
		cart.CodeVenueNotAccepting,
		cart.CodeItemUnavailable,
		cart.CodeOptionUnavailable,
		cart.CodePriceChanged,
		cart.CodeOutOfDeliveryZone,
		cart.CodeMinOrderNotMet,
	}, codes(result.Issues))
	assert.False(t, result.IsValid)
}

// Флаг accept_price_changes снимает подтверждаемые проблемы и только их.
func TestAcceptsPriceChanges(t *testing.T) {
	t.Parallel()

	line := lineOK()
	line.AddedUnitPriceMinor = 45000
	line.IsAvailable = false

	result := cart.Validate(cart.ValidationInput{Venue: venueOK(), Cart: cartWith(line)})

	kept := cart.AcceptsPriceChanges(result.Issues)

	assert.Equal(t, []string{cart.CodeItemUnavailable}, codes(kept),
		"блокирующая проблема флагом не снимается")
}

// Суммы считаются по актуальным ценам и с учётом количества.
func TestValidateAmounts(t *testing.T) {
	t.Parallel()

	first := lineOK()
	first.Qty = 2
	first.UnitPriceMinor = 52000
	first.AddedUnitPriceMinor = 52000

	second := lineOK()
	second.MenuItemID = itemID(2)
	second.Qty = 3
	second.UnitPriceMinor = 19000
	second.AddedUnitPriceMinor = 19000

	near := catalog.Point{Lat: 55.7361, Lon: 37.6252}

	result := cart.Validate(cart.ValidationInput{
		Venue:    venueOK(),
		Cart:     cartWith(first, second),
		Delivery: &near,
	})

	assert.Equal(t, int64(2*52000+3*19000), result.ItemsAmountMinor)
	assert.Equal(t, int64(14900), result.DeliveryFeeMinor)
	assert.Equal(t, result.ItemsAmountMinor+14900, result.TotalAmountMinor)
}
