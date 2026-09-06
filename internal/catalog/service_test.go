package catalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/catalog"
	"github.com/emotionlessdev/avito-kitchen/internal/paging"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVenues — хранилище каталога в памяти. Логика выдачи не должна требовать
// Postgres: репозиторий проверяется интеграционными тестами на настоящей базе
// (этап 11), а правила показа — здесь.
type fakeVenues struct {
	venues []catalog.Venue
	menu   catalog.Menu
	err    error
}

func (f *fakeVenues) ListVenues(_ context.Context, nameQuery string) ([]catalog.Venue, error) {
	if f.err != nil {
		return nil, f.err
	}

	if nameQuery == "" {
		return f.venues, nil
	}

	var matched []catalog.Venue

	for i := range f.venues {
		if f.venues[i].Name == nameQuery {
			matched = append(matched, f.venues[i])
		}
	}

	return matched, nil
}

func (f *fakeVenues) GetVenue(_ context.Context, id uuid.UUID) (catalog.Venue, error) {
	for i := range f.venues {
		if f.venues[i].ID == id {
			return f.venues[i], nil
		}
	}

	return catalog.Venue{}, catalog.ErrVenueNotFound
}

func (f *fakeVenues) GetMenu(_ context.Context, _ uuid.UUID, _ bool) (catalog.Menu, error) {
	return f.menu, nil
}

func (f *fakeVenues) SchedulesByVenueIDs(
	_ context.Context, ids []uuid.UUID,
) (map[uuid.UUID]catalog.Schedule, error) {
	schedules := make(map[uuid.UUID]catalog.Schedule, len(ids))

	for _, id := range ids {
		for i := range f.venues {
			if f.venues[i].ID == id {
				schedules[id] = f.venues[i].Schedule
			}
		}
	}

	return schedules, nil
}

func venueID(n int) uuid.UUID {
	return uuid.MustParse("01890000-0001-7000-8000-00000000000" + string(rune('0'+n)))
}

// Понедельник, 13:00 по Москве: «дневное» заведение открыто, «ночное» — нет.
func mondayNoon(t *testing.T) time.Time {
	t.Helper()

	return time.Date(2026, 9, 7, 13, 0, 0, 0, mustLoad(t, "Europe/Moscow"))
}

func dayVenue(t *testing.T, id uuid.UUID, name string, point catalog.Point) catalog.Venue {
	t.Helper()

	moscow := mustLoad(t, "Europe/Moscow")
	intervals := make([]catalog.WorkInterval, 0, 7)

	for day := catalog.Monday; day <= catalog.Sunday; day++ {
		intervals = append(intervals, catalog.WorkInterval{
			Weekday: day, Opens: hhmm(t, "10:00"), Closes: hhmm(t, "23:00"),
		})
	}

	return catalog.Venue{
		ID: id, Name: name, Point: point, Timezone: "Europe/Moscow",
		Status: catalog.StatusActive, IsAcceptingOrders: true,
		Schedule: catalog.Schedule{Location: moscow, Intervals: intervals},
	}
}

func nightVenue(t *testing.T, id uuid.UUID, name string, point catalog.Point) catalog.Venue {
	t.Helper()

	moscow := mustLoad(t, "Europe/Moscow")
	intervals := make([]catalog.WorkInterval, 0, 7)

	for day := catalog.Monday; day <= catalog.Sunday; day++ {
		intervals = append(intervals, catalog.WorkInterval{
			Weekday: day, Opens: hhmm(t, "20:00"), Closes: hhmm(t, "04:00"),
		})
	}

	return catalog.Venue{
		ID: id, Name: name, Point: point, Timezone: "Europe/Moscow",
		Status: catalog.StatusActive, IsAcceptingOrders: true,
		Schedule: catalog.Schedule{Location: moscow, Intervals: intervals},
	}
}

func TestServiceListVenues(t *testing.T) {
	t.Parallel()

	center := catalog.Point{Lat: 55.7402, Lon: 37.6280}
	near := dayVenue(t, venueID(1), "Ближнее", center)
	far := dayVenue(t, venueID(2), "Дальнее", catalog.Point{Lat: 55.7998, Lon: 37.5350})
	night := nightVenue(t, venueID(3), "Ночное", center)

	repo := &fakeVenues{venues: []catalog.Venue{near, far, night}}
	service := catalog.NewService(repo, catalog.WithClock(func() time.Time { return mondayNoon(t) }))

	t.Run("открытость считается на момент запроса", func(t *testing.T) {
		t.Parallel()

		page, err := service.ListVenues(t.Context(), catalog.ListParams{Limit: 10})
		require.NoError(t, err)
		require.Len(t, page.Items, 3)

		open := map[string]bool{}
		for i := range page.Items {
			open[page.Items[i].Name] = page.Items[i].IsOpenNow
		}

		assert.True(t, open["Ближнее"])
		assert.False(t, open["Ночное"], "ночное заведение днём закрыто")
	})

	t.Run("закрытому заведению проставлено время открытия", func(t *testing.T) {
		t.Parallel()

		page, err := service.ListVenues(t.Context(), catalog.ListParams{Limit: 10})
		require.NoError(t, err)

		for i := range page.Items {
			if page.Items[i].Name != "Ночное" {
				continue
			}

			require.NotNil(t, page.Items[i].NextOpenAt, "закрытое заведение обязано сказать, когда откроется")
			assert.Equal(t, 20, page.Items[i].NextOpenAt.In(mustLoad(t, "Europe/Moscow")).Hour())
		}
	})

	t.Run("open_now убирает закрытые", func(t *testing.T) {
		t.Parallel()

		page, err := service.ListVenues(t.Context(), catalog.ListParams{OpenNow: true, Limit: 10})
		require.NoError(t, err)

		names := make([]string, 0, len(page.Items))
		for i := range page.Items {
			names = append(names, page.Items[i].Name)
		}

		assert.Equal(t, []string{"Ближнее", "Дальнее"}, names)
	})

	t.Run("расстояние считается только с координатами", func(t *testing.T) {
		t.Parallel()

		withoutPoint, err := service.ListVenues(t.Context(), catalog.ListParams{Limit: 10})
		require.NoError(t, err)
		assert.Nil(t, withoutPoint.Items[0].DistanceM)

		withPoint, err := service.ListVenues(t.Context(), catalog.ListParams{Point: &center, Limit: 10})
		require.NoError(t, err)
		require.NotNil(t, withPoint.Items[0].DistanceM)
	})

	t.Run("сортировка по расстоянию без координат — ошибка, а не тихий алфавит", func(t *testing.T) {
		t.Parallel()

		_, err := service.ListVenues(t.Context(), catalog.ListParams{Sort: catalog.SortByDistance, Limit: 10})
		require.ErrorIs(t, err, catalog.ErrDistanceSortWithoutPoint)
	})

	t.Run("страницы идут по курсору и не повторяются", func(t *testing.T) {
		t.Parallel()

		first, err := service.ListVenues(t.Context(), catalog.ListParams{Limit: 2})
		require.NoError(t, err)
		require.Len(t, first.Items, 2)
		require.NotEmpty(t, first.NextCursor)

		second, err := service.ListVenues(t.Context(), catalog.ListParams{Limit: 2, Cursor: first.NextCursor})
		require.NoError(t, err)
		require.Len(t, second.Items, 1)
		assert.Empty(t, second.NextCursor, "последняя страница курсора не отдаёт")
		assert.NotEqual(t, first.Items[0].ID, second.Items[0].ID)
	})

	t.Run("подделанный курсор отклоняется", func(t *testing.T) {
		t.Parallel()

		_, err := service.ListVenues(t.Context(), catalog.ListParams{Limit: 2, Cursor: "!!!"})
		require.ErrorIs(t, err, paging.ErrBadCursor)
	})
}

func TestServiceGetVenue(t *testing.T) {
	t.Parallel()

	center := catalog.Point{Lat: 55.7402, Lon: 37.6280}
	repo := &fakeVenues{venues: []catalog.Venue{dayVenue(t, venueID(1), "Ближнее", center)}}
	service := catalog.NewService(repo, catalog.WithClock(func() time.Time { return mondayNoon(t) }))

	t.Run("найденное заведение приходит с расчётом открытости", func(t *testing.T) {
		t.Parallel()

		view, err := service.GetVenue(t.Context(), venueID(1), nil)
		require.NoError(t, err)
		assert.True(t, view.IsOpenNow)
		assert.Nil(t, view.NextOpenAt, "у открытого заведения обещать открытие незачем")
	})

	t.Run("чужой идентификатор — не найдено", func(t *testing.T) {
		t.Parallel()

		_, err := service.GetVenue(t.Context(), venueID(9), nil)
		require.ErrorIs(t, err, catalog.ErrVenueNotFound)
	})
}

func TestServiceOpenNowByVenueIDs(t *testing.T) {
	t.Parallel()

	center := catalog.Point{Lat: 55.7402, Lon: 37.6280}
	repo := &fakeVenues{venues: []catalog.Venue{
		dayVenue(t, venueID(1), "Дневное", center),
		nightVenue(t, venueID(2), "Ночное", center),
	}}
	service := catalog.NewService(repo, catalog.WithClock(func() time.Time { return mondayNoon(t) }))

	open, err := service.OpenNowByVenueIDs(t.Context(), []uuid.UUID{venueID(1), venueID(2)})
	require.NoError(t, err)

	assert.True(t, open[venueID(1)])
	assert.False(t, open[venueID(2)])
}
