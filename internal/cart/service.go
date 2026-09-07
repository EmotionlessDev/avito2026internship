package cart

import (
	"context"
	"fmt"

	"github.com/emotionlessdev/avito-kitchen/internal/catalog"

	"github.com/google/uuid"
)

// Store — то, что сервису нужно от хранилища корзины.
type Store interface {
	UserExists(ctx context.Context, userID uuid.UUID) (bool, error)
	Get(ctx context.Context, userID uuid.UUID) (Cart, error)
	MenuItemForAdd(ctx context.Context, itemID uuid.UUID) (MenuItem, error)
	AddLine(ctx context.Context, userID uuid.UUID, item MenuItem,
		optionIDs []uuid.UUID, qty int, unitPrice int64) error
	UpdateQty(ctx context.Context, userID, lineID uuid.UUID, qty int) error
	DeleteLine(ctx context.Context, userID, lineID uuid.UUID) error
	Clear(ctx context.Context, userID uuid.UUID) error
}

// Venues — источник сведений о заведении для каскада: открыто ли оно сейчас,
// принимает ли заказы, где находится. Реализует его каталог — там живут
// расписание и геометрия.
type Venues interface {
	GetVenue(ctx context.Context, id uuid.UUID, point *catalog.Point) (catalog.VenueView, error)
}

// Service — операции над корзиной.
type Service struct {
	store  Store
	venues Venues
}

// NewService собирает сервис корзины.
func NewService(store Store, venues Venues) *Service {
	return &Service{store: store, venues: venues}
}

// AddRequest — что положить в корзину.
type AddRequest struct {
	ItemID    uuid.UUID
	Qty       int
	OptionIDs []uuid.UUID
}

// EnsureUser проверяет, что пользователь известен платформе.
//
// Отдельный шаг, потому что X-User-Id — заглушка вместо авторизации:
// пока её нет, единственная проверка «кто это» — существование строки.
func (s *Service) EnsureUser(ctx context.Context, userID uuid.UUID) error {
	exists, err := s.store.UserExists(ctx, userID)
	if err != nil {
		return err
	}

	if !exists {
		return ErrUnknownUser
	}

	return nil
}

// Get отдаёт корзину с актуальными ценами.
func (s *Service) Get(ctx context.Context, userID uuid.UUID) (Cart, error) {
	return s.store.Get(ctx, userID)
}

// AddItem кладёт позицию в корзину.
//
// Проверки идут до записи: позиция должна существовать и продаваться,
// заведение — совпадать с заведением корзины, а набор модификаторов —
// удовлетворять группам позиции.
func (s *Service) AddItem(ctx context.Context, userID uuid.UUID, request AddRequest) (Cart, error) {
	item, err := s.store.MenuItemForAdd(ctx, request.ItemID)
	if err != nil {
		return Cart{}, err
	}

	if !item.IsAvailable {
		return Cart{}, fmt.Errorf("%w: позиция снята с продажи", ErrMenuItemNotFound)
	}

	unitPrice, err := unitPriceWithOptions(item, request.OptionIDs)
	if err != nil {
		return Cart{}, err
	}

	if err := s.store.AddLine(ctx, userID, item, request.OptionIDs, request.Qty, unitPrice); err != nil {
		return Cart{}, err
	}

	return s.store.Get(ctx, userID)
}

// UpdateQty меняет количество позиции.
func (s *Service) UpdateQty(ctx context.Context, userID, lineID uuid.UUID, qty int) (Cart, error) {
	if err := s.store.UpdateQty(ctx, userID, lineID, qty); err != nil {
		return Cart{}, err
	}

	return s.store.Get(ctx, userID)
}

// DeleteLine убирает позицию из корзины.
func (s *Service) DeleteLine(ctx context.Context, userID, lineID uuid.UUID) (Cart, error) {
	if err := s.store.DeleteLine(ctx, userID, lineID); err != nil {
		return Cart{}, err
	}

	return s.store.Get(ctx, userID)
}

// Clear очищает корзину.
func (s *Service) Clear(ctx context.Context, userID uuid.UUID) error {
	return s.store.Clear(ctx, userID)
}

// Validate прогоняет каскад проверок над текущей корзиной.
//
// Сервис только собирает снимок — решения принимает чистая функция Validate,
// поэтому все коды проблем проверяются юнит-тестами без базы.
func (s *Service) Validate(
	ctx context.Context, userID uuid.UUID, delivery *catalog.Point,
) (Result, error) {
	current, err := s.store.Get(ctx, userID)
	if err != nil {
		return Result{}, err
	}

	if current.IsEmpty() || current.VenueID == nil {
		// Пустой корзине нечего проверять: ни заведения, ни позиций.
		return Validate(ValidationInput{Cart: current}), nil
	}

	view, err := s.venues.GetVenue(ctx, *current.VenueID, nil)
	if err != nil {
		return Result{}, err
	}

	return Validate(ValidationInput{
		Venue:    venueFrom(view),
		Cart:     current,
		Delivery: delivery,
	}), nil
}

func venueFrom(view catalog.VenueView) Venue {
	return Venue{
		ID:                  view.ID,
		IsOpenNow:           view.IsOpenNow,
		NextOpenAt:          view.NextOpenAt,
		IsAcceptingOrders:   view.IsAcceptingOrders,
		Point:               view.Point,
		DeliveryRadiusM:     view.DeliveryRadiusM,
		MinOrderAmountMinor: view.MinOrderAmountMinor,
		DeliveryFeeMinor:    view.DeliveryFeeMinor,
	}
}

// unitPriceWithOptions считает цену за штуку и заодно проверяет выбор
// модификаторов: чужая опция, слишком мало или слишком много выборов —
// это ошибка запроса, а не проблема корзины.
func unitPriceWithOptions(item MenuItem, optionIDs []uuid.UUID) (int64, error) {
	chosen := make(map[uuid.UUID]struct{}, len(optionIDs))
	for _, id := range optionIDs {
		if _, duplicate := chosen[id]; duplicate {
			return 0, fmt.Errorf("%w: модификатор выбран дважды", ErrInvalidOptions)
		}

		chosen[id] = struct{}{}
	}

	price := item.PriceMinor
	matched := 0

	for _, group := range item.Groups {
		count := 0

		for id := range chosen {
			delta, belongs := group.Options[id]
			if !belongs {
				continue
			}

			count++
			matched++
			price += delta
		}

		if count < group.MinChoices {
			return 0, fmt.Errorf("%w: в группе %q нужно выбрать минимум %d",
				ErrInvalidOptions, group.Name, group.MinChoices)
		}

		if count > group.MaxChoices {
			return 0, fmt.Errorf("%w: в группе %q можно выбрать максимум %d",
				ErrInvalidOptions, group.Name, group.MaxChoices)
		}
	}

	// Опция, не принадлежащая ни одной группе позиции, — почти всегда
	// перепутанный идентификатор. Молча игнорировать её нельзя: цена
	// посчиталась бы не та, что ожидает клиент.
	if matched != len(chosen) {
		return 0, fmt.Errorf("%w: выбран модификатор, которого нет у позиции", ErrInvalidOptions)
	}

	return price, nil
}
