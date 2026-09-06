package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/paging"

	"github.com/google/uuid"
)

// SortMode — порядок выдачи каталога.
type SortMode string

// Поддерживаемые порядки. Значения совпадают с enum параметра sort в спеке.
const (
	SortByName     SortMode = "name"
	SortByDistance SortMode = "distance"
)

// ErrDistanceSortWithoutPoint — попросили сортировку по расстоянию, не передав
// координаты. Молча сортировать по названию нельзя: пользователь увидел бы
// не тот порядок и не узнал бы об этом.
var ErrDistanceSortWithoutPoint = errors.New("сортировка по расстоянию требует координат")

// ListParams — запрос к каталогу.
type ListParams struct {
	// Point — координаты пользователя; nil, если клиент их не передал.
	Point   *Point
	Query   string
	OpenNow bool
	Sort    SortMode
	Cursor  string
	Limit   int
}

// VenuePage — страница выдачи каталога.
type VenuePage struct {
	Items      []VenueView
	NextCursor string
}

// VenueSource — то, что сервису нужно от хранилища каталога. Интерфейс
// объявлен у потребителя, а реализует его Repository: так логика выдачи
// тестируется без Postgres, а репозиторий не обязан ничего знать про сервис.
type VenueSource interface {
	ListVenues(ctx context.Context, nameQuery string) ([]Venue, error)
	GetVenue(ctx context.Context, id uuid.UUID) (Venue, error)
	GetMenu(ctx context.Context, venueID uuid.UUID, includeUnavailable bool) (Menu, error)
	SchedulesByVenueIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Schedule, error)
}

// Service отвечает на вопросы каталога: что показать, открыто ли оно сейчас
// и как далеко ехать.
type Service struct {
	repo VenueSource
	// now подменяется в тестах: «открыто сейчас» обязано быть проверяемым,
	// а не зависеть от того, в котором часу запущен CI.
	now func() time.Time
}

// ServiceOption настраивает сервис при сборке. Имя с приставкой Service —
// потому что Option в этом пакете уже занят модификатором блюда.
type ServiceOption func(*Service)

// WithClock подменяет часы сервиса. Нужен тестам: расчёт «открыто сейчас»
// иначе зависел бы от времени прогона.
func WithClock(now func() time.Time) ServiceOption {
	return func(s *Service) { s.now = now }
}

// NewService собирает сервис каталога.
func NewService(repo VenueSource, opts ...ServiceOption) *Service {
	service := &Service{repo: repo, now: time.Now}

	for _, opt := range opts {
		opt(service)
	}

	return service
}

// ListVenues возвращает страницу каталога с расчётом «открыто сейчас»
// и расстояния.
//
// Фильтрация по расписанию и сортировка по расстоянию делаются здесь, а не
// в SQL: правило «открыто сейчас» с работой через полночь и чужими часовыми
// поясами живёт в одной протестированной функции. Цена решения — выборка
// всех опубликованных заведений на запрос; при закрытом пуле заведений это
// дёшево, а точка масштабирования известна: материализованный флаг
// в витрине и KNN-индекс PostGIS для расстояния.
func (s *Service) ListVenues(ctx context.Context, params ListParams) (VenuePage, error) {
	if params.Sort == SortByDistance && params.Point == nil {
		return VenuePage{}, ErrDistanceSortWithoutPoint
	}

	offset, err := paging.DecodeOffset(params.Cursor)
	if err != nil {
		return VenuePage{}, err
	}

	venues, err := s.repo.ListVenues(ctx, strings.TrimSpace(params.Query))
	if err != nil {
		return VenuePage{}, err
	}

	now := s.now()
	views := make([]VenueView, 0, len(venues))

	for i := range venues {
		view := s.view(venues[i], now, params.Point)
		if params.OpenNow && !view.IsOpenNow {
			continue
		}

		views = append(views, view)
	}

	sortVenues(views, params.Sort)

	items, next := paging.Page(views, offset, params.Limit)

	return VenuePage{Items: items, NextCursor: next}, nil
}

// GetVenue отдаёт карточку заведения.
func (s *Service) GetVenue(ctx context.Context, id uuid.UUID, point *Point) (VenueView, error) {
	venue, err := s.repo.GetVenue(ctx, id)
	if err != nil {
		return VenueView{}, err
	}

	return s.view(venue, s.now(), point), nil
}

// GetMenu отдаёт меню заведения.
func (s *Service) GetMenu(ctx context.Context, venueID uuid.UUID, includeUnavailable bool) (Menu, error) {
	menu, err := s.repo.GetMenu(ctx, venueID, includeUnavailable)
	if err != nil {
		return Menu{}, fmt.Errorf("меню заведения: %w", err)
	}

	return menu, nil
}

// OpenNowByVenueIDs говорит, какие из заведений открыты в текущий момент.
// Нужен поиску: витрина не хранит расписание, а показать признак надо.
func (s *Service) OpenNowByVenueIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	schedules, err := s.repo.SchedulesByVenueIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	now := s.now()
	open := make(map[uuid.UUID]bool, len(schedules))

	for id, schedule := range schedules {
		open[id] = schedule.IsOpenAt(now)
	}

	return open, nil
}

func (s *Service) view(venue Venue, now time.Time, point *Point) VenueView {
	view := VenueView{Venue: venue, IsOpenNow: venue.Schedule.IsOpenAt(now)}

	if !view.IsOpenNow {
		if nextOpen, ok := venue.Schedule.NextOpenAt(now); ok {
			view.NextOpenAt = &nextOpen
		}
	}

	if point != nil {
		distance := DistanceM(*point, venue.Point)
		view.DistanceM = &distance
	}

	return view
}

// sortVenues раскладывает выдачу в запрошенном порядке. Открытые заведения
// идут первыми в обоих порядках: закрытое заведение рядом с домом бесполезно
// прямо сейчас, а показать его всё же надо — вдруг пользователь подождёт.
func sortVenues(views []VenueView, mode SortMode) {
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]

		if left.IsOpenNow != right.IsOpenNow {
			return left.IsOpenNow
		}

		if mode == SortByDistance && left.DistanceM != nil && right.DistanceM != nil &&
			*left.DistanceM != *right.DistanceM {
			return *left.DistanceM < *right.DistanceM
		}

		if left.Name != right.Name {
			return left.Name < right.Name
		}

		return left.ID.String() < right.ID.String()
	})
}
