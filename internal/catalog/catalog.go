// Package catalog — заведения и меню: домен, репозиторий и сервис выдачи.
//
// Каталог здесь — read-model: пользователь ищет, где поесть, а не редактирует
// справочник. Всё, что меняет данные каталога, живёт в merchant-контуре
// (импорт меню, стоп-листы) и появится на этапе 4.
package catalog

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Статусы заведения. Значения совпадают с CHECK в миграции 0001_venues_menu.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusDraft     = "draft"
)

// ErrVenueNotFound — заведения нет или оно не опубликовано. Для клиента это
// одно и то же: черновик чужого заведения не должен отличаться от опечатки в id.
var ErrVenueNotFound = errors.New("заведение не найдено")

// Venue — заведение со всем, что нужно и карточке, и проверке заказа.
type Venue struct {
	ID                  uuid.UUID
	Slug                string
	Name                string
	AddressText         string
	Point               Point
	Timezone            string
	Status              string
	IsAcceptingOrders   bool
	MinOrderAmountMinor int64
	DeliveryFeeMinor    int64
	DeliveryRadiusM     int
	AvgPrepMinutes      int
	Schedule            Schedule
}

// VenueView — заведение, дополненное тем, что зависит от момента и от места
// пользователя: открыто ли сейчас, когда откроется, как далеко ехать.
//
// Отдельный тип, а не поля в Venue: Venue — это то, что лежит в базе,
// а VenueView — то, что видит конкретный пользователь в конкретную минуту.
type VenueView struct {
	Venue

	IsOpenNow  bool
	NextOpenAt *time.Time
	DistanceM  *int
}

// Menu — меню заведения по категориям.
type Menu struct {
	VenueID    uuid.UUID
	Categories []Category
}

// Category — раздел меню с позициями в порядке показа.
type Category struct {
	ID         uuid.UUID
	ExternalID string
	Name       string
	Position   int
	Items      []Item
}

// Item — позиция меню вместе с её модификаторами.
type Item struct {
	ID           uuid.UUID
	ExternalID   string
	Name         string
	Description  string
	PriceMinor   int64
	WeightGrams  *int
	Position     int
	IsAvailable  bool
	OptionGroups []OptionGroup
}

// OptionGroup — группа модификаторов: «Размер», «Добавки».
type OptionGroup struct {
	ID         uuid.UUID
	ExternalID string
	Name       string
	MinChoices int
	MaxChoices int
	Position   int
	Options    []Option
}

// Option — один модификатор и его надбавка к цене.
type Option struct {
	ID              uuid.UUID
	ExternalID      string
	Name            string
	PriceDeltaMinor int64
	Position        int
	IsAvailable     bool
}
