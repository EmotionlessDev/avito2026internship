package cart

import (
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/catalog"

	"github.com/google/uuid"
)

// Коды проблем корзины. Полный словарь из docs/DESIGN.md §2: каждый код —
// отдельная ветка каскада и отдельный тест.
const (
	CodeItemUnavailable   = "ITEM_UNAVAILABLE"
	CodeOptionUnavailable = "OPTION_UNAVAILABLE"
	CodePriceChanged      = "PRICE_CHANGED"
	CodeVenueClosed       = "VENUE_CLOSED"
	CodeVenueNotAccepting = "VENUE_NOT_ACCEPTING"
	CodeOutOfDeliveryZone = "OUT_OF_DELIVERY_ZONE"
	CodeMinOrderNotMet    = "MIN_ORDER_NOT_MET"
	CodeCartMixedVenues   = "CART_MIXED_VENUES"
)

// Severity различает «нельзя» и «переспроси».
const (
	// SeverityBlocking гасит кнопку «Заказать».
	SeverityBlocking = "blocking"
	// SeverityConfirmable показывает диалог и снимается флагом
	// accept_price_changes при оформлении.
	SeverityConfirmable = "confirmable"
)

// Issue — одна проблема корзины. Набор заполненных полей зависит от кода:
// у денежных проблем это суммы, у товарных — позиция или опция.
type Issue struct {
	Code     string
	Severity string
	Message  string

	ItemID     *uuid.UUID
	ItemName   string
	OptionID   *uuid.UUID
	OptionName string

	OldPriceMinor   *int64
	NewPriceMinor   *int64
	MinOrderMinor   *int64
	MissingMinor    *int64
	NextOpenAt      *time.Time
	DistanceM       *int
	DeliveryRadiusM *int
}

// Venue — то, что каскад знает о заведении на момент проверки.
type Venue struct {
	ID                  uuid.UUID
	IsOpenNow           bool
	NextOpenAt          *time.Time
	IsAcceptingOrders   bool
	Point               catalog.Point
	DeliveryRadiusM     int
	MinOrderAmountMinor int64
	DeliveryFeeMinor    int64
}

// ValidationInput — снимок, по которому считается диагноз. Ни базы, ни HTTP:
// каскад — чистая функция, поэтому все восемь кодов проверяются юнит-тестами.
type ValidationInput struct {
	Venue Venue
	Cart  Cart
	// Delivery — адрес доставки; nil, если клиент его ещё не ввёл. Без него
	// не проверяются зона доставки и стоимость доставки.
	Delivery *catalog.Point
}

// Result — ответ валидации: суммы плюс список проблем.
type Result struct {
	IsValid          bool
	VenueID          *uuid.UUID
	ItemsAmountMinor int64
	DeliveryFeeMinor int64
	TotalAmountMinor int64
	Issues           []Issue
}

// Validate прогоняет каскад проверок из docs/DESIGN.md §2.
//
// Каскад не останавливается на первой проблеме: пользователю нужен полный
// список, иначе он будет чинить корзину по одной ошибке за запрос. Порядок
// проблем — от заведения к позициям и деньгам: так их и читает интерфейс.
func Validate(input ValidationInput) Result {
	result := Result{
		ItemsAmountMinor: input.Cart.ItemsAmountMinor(),
		VenueID:          input.Cart.VenueID,
	}

	if input.Delivery != nil {
		result.DeliveryFeeMinor = input.Venue.DeliveryFeeMinor
	}

	result.TotalAmountMinor = result.ItemsAmountMinor + result.DeliveryFeeMinor

	// Пустая корзина — не проблема, а отсутствие предмета разговора:
	// проверять нечего, оформлять тоже.
	if input.Cart.IsEmpty() {
		return result
	}

	result.Issues = append(result.Issues, venueIssues(input.Venue)...)
	result.Issues = append(result.Issues, lineIssues(input.Cart)...)
	result.Issues = append(result.Issues, deliveryIssues(input)...)
	result.Issues = append(result.Issues, minOrderIssue(input.Venue, result.ItemsAmountMinor)...)

	result.IsValid = !hasBlocking(result.Issues)

	return result
}

func venueIssues(venue Venue) []Issue {
	var issues []Issue

	if !venue.IsOpenNow {
		issues = append(issues, Issue{
			Code:       CodeVenueClosed,
			Severity:   SeverityBlocking,
			Message:    "Заведение сейчас закрыто",
			NextOpenAt: venue.NextOpenAt,
		})
	}

	// Пауза приёма — отдельная причина от закрытия: заведение работает,
	// но кухня перегружена и заказы не берёт (сценарий M7).
	if !venue.IsAcceptingOrders {
		issues = append(issues, Issue{
			Code:     CodeVenueNotAccepting,
			Severity: SeverityBlocking,
			Message:  "Заведение временно не принимает заказы",
		})
	}

	return issues
}

func lineIssues(cart Cart) []Issue {
	var issues []Issue

	for i := range cart.Lines {
		line := cart.Lines[i]
		itemID := line.MenuItemID

		if !line.IsAvailable {
			issues = append(issues, Issue{
				Code:     CodeItemUnavailable,
				Severity: SeverityBlocking,
				Message:  "Закончилось",
				ItemID:   &itemID,
				ItemName: line.Name,
			})
		}

		for j := range line.Options {
			option := line.Options[j]
			if option.IsAvailable {
				continue
			}

			optionID := option.OptionID
			issues = append(issues, Issue{
				Code:       CodeOptionUnavailable,
				Severity:   SeverityBlocking,
				Message:    "Опция недоступна",
				ItemID:     &itemID,
				ItemName:   line.Name,
				OptionID:   &optionID,
				OptionName: option.Name,
			})
		}

		// Цена сравнивается за единицу: количество на факт подорожания
		// не влияет, а пользователю показывают цену за штуку.
		if line.UnitPriceMinor != line.AddedUnitPriceMinor {
			oldPrice := line.AddedUnitPriceMinor
			newPrice := line.UnitPriceMinor

			issues = append(issues, Issue{
				Code:          CodePriceChanged,
				Severity:      SeverityConfirmable,
				Message:       "Цена изменилась",
				ItemID:        &itemID,
				ItemName:      line.Name,
				OldPriceMinor: &oldPrice,
				NewPriceMinor: &newPrice,
			})
		}
	}

	return issues
}

func deliveryIssues(input ValidationInput) []Issue {
	if input.Delivery == nil {
		return nil
	}

	distance := catalog.DistanceM(*input.Delivery, input.Venue.Point)
	if distance <= input.Venue.DeliveryRadiusM {
		return nil
	}

	radius := input.Venue.DeliveryRadiusM

	return []Issue{{
		Code:            CodeOutOfDeliveryZone,
		Severity:        SeverityBlocking,
		Message:         "Адрес вне зоны доставки заведения",
		DistanceM:       &distance,
		DeliveryRadiusM: &radius,
	}}
}

// minOrderIssue считает нехватку до минимальной суммы.
//
// Минимум сравнивается со стоимостью блюд, без доставки: иначе плата
// за доставку помогала бы «набрать» минимум, а это не то, что обещает
// заведение.
func minOrderIssue(venue Venue, itemsAmountMinor int64) []Issue {
	if itemsAmountMinor >= venue.MinOrderAmountMinor {
		return nil
	}

	minOrder := venue.MinOrderAmountMinor
	missing := venue.MinOrderAmountMinor - itemsAmountMinor

	return []Issue{{
		Code:          CodeMinOrderNotMet,
		Severity:      SeverityBlocking,
		Message:       "Не набрана минимальная сумма заказа",
		MinOrderMinor: &minOrder,
		MissingMinor:  &missing,
	}}
}

func hasBlocking(issues []Issue) bool {
	for i := range issues {
		if issues[i].Severity == SeverityBlocking {
			return true
		}
	}

	return false
}

// AcceptsPriceChanges убирает из списка проблемы, которые пользователь
// подтвердил флагом accept_price_changes. Блокирующие проблемы этим флагом
// не снимаются — на то они и блокирующие.
func AcceptsPriceChanges(issues []Issue) []Issue {
	kept := make([]Issue, 0, len(issues))

	for i := range issues {
		if issues[i].Severity == SeverityConfirmable {
			continue
		}

		kept = append(kept, issues[i])
	}

	return kept
}
