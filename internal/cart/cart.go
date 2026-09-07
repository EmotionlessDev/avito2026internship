// Package cart — корзина пользователя и каскад её проверок.
//
// Корзина у пользователя одна и привязана к одному заведению: так работает
// большинство фудтех-сервисов, а технически ограничение снимается заменой
// UNIQUE(user_id) на UNIQUE(user_id, venue_id) (README.md, раздел «Осознанные упрощения»).
//
// Главное в пакете — не CRUD, а каскад валидации: пользователь должен узнать
// о проблеме до нажатия «Заказать», и каждая проблема приходит кодом, по
// которому клиент рисует конкретное действие.
package cart

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Ошибки домена. Перевод в HTTP-статусы живёт в слое API: домен про коды
// ответов не знает.
var (
	// ErrUnknownUser — пользователь из X-User-Id неизвестен платформе.
	ErrUnknownUser = errors.New("пользователь не найден")
	// ErrMenuItemNotFound — позиции нет, она архивирована или принадлежит
	// заведению, которого нет в каталоге.
	ErrMenuItemNotFound = errors.New("позиция меню не найдена")
	// ErrCartItemNotFound — позиции нет в корзине этого пользователя.
	ErrCartItemNotFound = errors.New("позиция корзины не найдена")
	// ErrMixedVenues — попытка сложить в одну корзину блюда разных заведений.
	ErrMixedVenues = errors.New("в корзине блюда другого заведения")
	// ErrInvalidOptions — выбранные модификаторы не соответствуют группам
	// позиции: чужая опция, слишком мало или слишком много выборов.
	ErrInvalidOptions = errors.New("некорректный набор модификаторов")
)

// Cart — корзина пользователя вместе с пересчитанными на момент чтения ценами.
type Cart struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	VenueID   *uuid.UUID
	VenueName string
	Lines     []Line
	UpdatedAt time.Time
}

// Line — позиция корзины.
//
// Хранит две цены: ту, что была на момент добавления, и текущую. Разница между
// ними — это сценарий U7: «цена выросла с 450 до 520 ₽, продолжить?».
type Line struct {
	ID                  uuid.UUID
	MenuItemID          uuid.UUID
	Name                string
	Qty                 int
	AddedUnitPriceMinor int64
	UnitPriceMinor      int64
	IsAvailable         bool
	Options             []LineOption
}

// LineOption — выбранный модификатор позиции корзины.
type LineOption struct {
	OptionID        uuid.UUID
	GroupID         uuid.UUID
	Name            string
	PriceDeltaMinor int64
	IsAvailable     bool
}

// TotalMinor — стоимость строки по текущей цене.
func (l Line) TotalMinor() int64 {
	return l.UnitPriceMinor * int64(l.Qty)
}

// ItemsAmountMinor — стоимость блюд в корзине по текущим ценам.
func (c Cart) ItemsAmountMinor() int64 {
	var total int64

	for i := range c.Lines {
		total += c.Lines[i].TotalMinor()
	}

	return total
}

// MixedVenuesError — в корзину кладут блюдо не того заведения.
//
// Ошибка несёт название заведения, которое уже занимает корзину: клиенту
// нужно спросить «очистить корзину «Хачапури и вино»?», а не абстрактное
// «очистить корзину» — иначе пользователь не поймёт, что именно он теряет.
type MixedVenuesError struct {
	VenueName string
}

func (e MixedVenuesError) Error() string {
	return ErrMixedVenues.Error() + ": " + e.VenueName
}

// Is связывает конкретную ошибку с доменным маркером.
func (e MixedVenuesError) Is(target error) bool { return errors.Is(target, ErrMixedVenues) }

// IsEmpty сообщает, что оформлять нечего.
func (c Cart) IsEmpty() bool {
	return len(c.Lines) == 0
}
