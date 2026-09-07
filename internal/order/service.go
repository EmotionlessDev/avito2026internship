package order

import (
	"context"
	"errors"
	"fmt"

	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/catalog"

	"github.com/google/uuid"
)

// Store — то, что сервису нужно от хранилища заказов.
type Store interface {
	Create(ctx context.Context, draft Draft) (Order, bool, error)
	ByIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (Order, bool, error)
	ByUser(ctx context.Context, userID, orderID uuid.UUID) (Order, error)
	ByVenue(ctx context.Context, venueID, orderID uuid.UUID) (Order, error)
	ListByUser(ctx context.Context, userID uuid.UUID, filter Filter) ([]Order, error)
	ListByVenue(ctx context.Context, venueID uuid.UUID, filter Filter) ([]Order, error)
}

// Carts — корзина, из которой собирается заказ.
type Carts interface {
	Get(ctx context.Context, userID uuid.UUID) (cart.Cart, error)
	Validate(ctx context.Context, userID uuid.UUID, delivery *catalog.Point) (cart.Result, error)
	Clear(ctx context.Context, userID uuid.UUID) error
}

// Venues — сведения о заведении, нужные заказу: часовой пояс для номера
// и название для события.
type Venues interface {
	GetVenue(ctx context.Context, id uuid.UUID, point *catalog.Point) (catalog.VenueView, error)
}

// Service — оформление и чтение заказов.
type Service struct {
	store  Store
	carts  Carts
	venues Venues
}

// NewService собирает сервис заказов.
func NewService(store Store, carts Carts, venues Venues) *Service {
	return &Service{store: store, carts: carts, venues: venues}
}

// CreateRequest — запрос на оформление.
type CreateRequest struct {
	IdempotencyKey     string
	Delivery           Delivery
	ContactPhone       string
	Comment            string
	AcceptPriceChanges bool
}

// Delivery — куда везти.
type Delivery struct {
	Address string
	Point   Point
}

// ValidationError — корзину нельзя оформить; несёт те же проблемы, что
// отдаёт /cart/validate, чтобы клиенту не нужна была вторая ветка разбора.
type ValidationError struct {
	Issues []cart.Issue
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: проблем — %d", ErrCartNotValid.Error(), len(e.Issues))
}

// Is связывает конкретную ошибку с доменным маркером.
func (e ValidationError) Is(target error) bool { return errors.Is(target, ErrCartNotValid) }

// Create оформляет заказ из корзины пользователя.
//
// Порядок важен: сначала каскад проверок, потом запись. Заказ, оформленный
// на закрытое заведение или на исчезнувшее блюдо, пришлось бы отменять
// компенсацией — дешевле не создавать его вовсе.
func (s *Service) Create(
	ctx context.Context, userID uuid.UUID, request CreateRequest,
) (Order, bool, error) {
	// Идемпотентность проверяется до всего остального. После успешного
	// оформления корзина пуста, и повтор, начатый с проверок, увидел бы
	// пустую корзину вместо уже созданного заказа — то есть ответил бы
	// ошибкой на честный ретрай сети.
	fingerprint := Fingerprint(request)

	existing, found, err := s.store.ByIdempotencyKey(ctx, userID, request.IdempotencyKey)
	if err != nil {
		return Order{}, false, err
	}

	if found {
		// Тот же ключ с другим телом — не ретрай, а ошибка клиента.
		// Вернуть старый заказ значит тихо проигнорировать новый адрес
		// или телефон, поэтому здесь 409, а не 200.
		if existing.Fingerprint != fingerprint {
			return Order{}, false, ErrIdempotencyConflict
		}

		return existing, false, nil
	}

	deliveryPoint := catalog.Point{Lat: request.Delivery.Point.Lat, Lon: request.Delivery.Point.Lon}

	validation, err := s.carts.Validate(ctx, userID, &deliveryPoint)
	if err != nil {
		return Order{}, false, err
	}

	if issues := blockingIssues(validation, request.AcceptPriceChanges); len(issues) > 0 {
		return Order{}, false, ValidationError{Issues: issues}
	}

	current, err := s.carts.Get(ctx, userID)
	if err != nil {
		return Order{}, false, err
	}

	if current.IsEmpty() || current.VenueID == nil {
		return Order{}, false, ValidationError{}
	}

	venue, err := s.venues.GetVenue(ctx, *current.VenueID, nil)
	if err != nil {
		return Order{}, false, err
	}

	draft := draftFrom(current, venue, validation, request)
	draft.Fingerprint = fingerprint

	created, isNew, err := s.store.Create(ctx, draft)
	if err != nil {
		return Order{}, false, err
	}

	// Корзину чистим только после успешной записи и только у нового заказа:
	// повтор с тем же ключом идемпотентности не должен ничего трогать.
	if isNew {
		if err := s.carts.Clear(ctx, userID); err != nil {
			return Order{}, false, err
		}
	}

	return created, isNew, nil
}

// Get отдаёт заказ пользователя.
func (s *Service) Get(ctx context.Context, userID, orderID uuid.UUID) (Order, error) {
	return s.store.ByUser(ctx, userID, orderID)
}

// List отдаёт историю заказов пользователя.
func (s *Service) List(ctx context.Context, userID uuid.UUID, filter Filter) ([]Order, error) {
	return s.store.ListByUser(ctx, userID, filter)
}

// GetForVenue отдаёт заказ заведения.
func (s *Service) GetForVenue(ctx context.Context, venueID, orderID uuid.UUID) (Order, error) {
	return s.store.ByVenue(ctx, venueID, orderID)
}

// ListForVenue отдаёт ленту заказов заведения.
func (s *Service) ListForVenue(
	ctx context.Context, venueID uuid.UUID, filter Filter,
) ([]Order, error) {
	return s.store.ListByVenue(ctx, venueID, filter)
}

// blockingIssues оставляет проблемы, которые мешают оформить заказ.
//
// Подтверждаемые проблемы (изменившаяся цена) снимаются флагом
// accept_price_changes — это ровно то согласие, которое клиент получает
// в диалоге «цена выросла, продолжить?».
func blockingIssues(result cart.Result, accepted bool) []cart.Issue {
	if accepted {
		return cart.AcceptsPriceChanges(result.Issues)
	}

	return result.Issues
}

func draftFrom(
	current cart.Cart, venue catalog.VenueView, validation cart.Result, request CreateRequest,
) Draft {
	items := make([]DraftItem, 0, len(current.Lines))

	for i := range current.Lines {
		line := current.Lines[i]

		optionIDs := make([]uuid.UUID, 0, len(line.Options))
		for j := range line.Options {
			optionIDs = append(optionIDs, line.Options[j].OptionID)
		}

		items = append(items, DraftItem{
			MenuItemID: line.MenuItemID,
			Name:       line.Name,
			PriceMinor: line.UnitPriceMinor,
			Qty:        line.Qty,
			TotalMinor: line.TotalMinor(),
			OptionIDs:  optionIDs,
		})
	}

	return Draft{
		UserID:           current.UserID,
		VenueID:          venue.ID,
		VenueName:        venue.Name,
		VenueTimezone:    venue.Timezone,
		ItemsAmountMinor: validation.ItemsAmountMinor,
		DeliveryFeeMinor: validation.DeliveryFeeMinor,
		TotalAmountMinor: validation.TotalAmountMinor,
		DeliveryAddress:  request.Delivery.Address,
		DeliveryPoint:    request.Delivery.Point,
		ContactPhone:     request.ContactPhone,
		Comment:          request.Comment,
		IdempotencyKey:   request.IdempotencyKey,
		Items:            items,
	}
}
