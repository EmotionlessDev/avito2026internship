package search_test

import (
	"context"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/catalog"
	"github.com/emotionlessdev/avito-kitchen/internal/search"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeIndex подменяет витрину: полнотекст и триграммы — работа Postgres,
// а вот когда звать второе и как склеивать результаты, решает сервис.
// Счётчики вызовов проверяют именно это решение.
type fakeIndex struct {
	fullText    []search.Item
	trigram     []search.Item
	trigramHits int
}

func (f *fakeIndex) FullText(_ context.Context, _ string, _ int) ([]search.Item, error) {
	return f.fullText, nil
}

func (f *fakeIndex) Trigram(_ context.Context, _ string, _ int) ([]search.Item, error) {
	f.trigramHits++

	return f.trigram, nil
}

// openVenues отвечает, что все заведения открыты: признак берётся у каталога,
// его собственные правила проверены там.
type openVenues struct {
	closed map[uuid.UUID]bool
}

func (o openVenues) OpenNowByVenueIDs(
	_ context.Context, ids []uuid.UUID,
) (map[uuid.UUID]bool, error) {
	state := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		state[id] = !o.closed[id]
	}

	return state, nil
}

func itemID(n int) uuid.UUID {
	return uuid.MustParse("01890000-0003-7000-8000-00000000000" + string(rune('0'+n)))
}

func venueID(n int) uuid.UUID {
	return uuid.MustParse("01890000-0001-7000-8000-00000000000" + string(rune('0'+n)))
}

func item(n int, name string, relevance float64, available bool) search.Item {
	return search.Item{
		ItemID: itemID(n), VenueID: venueID(1), ItemName: name,
		VenueName: "Хачапури и вино", CategoryName: "Хачапури",
		PriceMinor: 52000, IsAvailable: available, VenueIsAccepting: true,
		Lat: 55.7402, Lon: 37.6280, Relevance: relevance,
	}
}

func TestSearchRanksAvailableFirst(t *testing.T) {
	t.Parallel()

	index := &fakeIndex{fullText: []search.Item{
		item(1, "Хинкали с бараниной", 0.5, false),
		item(2, "Хачапури по-аджарски", 0.5, true),
		item(3, "Хачапури по-мегрельски", 0.4, true),
		item(4, "Хачапури по-имеретински", 0.3, true),
		item(5, "Кубдари", 0.2, true),
	}}

	service := search.NewService(index, openVenues{})

	page, err := service.Search(t.Context(), search.Query{Text: "хачапури", Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 5)

	assert.Equal(t, "Хачапури по-аджарски", page.Items[0].ItemName)
	assert.Equal(t, "Хинкали с бараниной", page.Items[len(page.Items)-1].ItemName,
		"недоступное опускается вниз, но из выдачи не исчезает")
	assert.Zero(t, index.trigramHits, "полнотекст нашёл достаточно — добор не нужен")
}

func TestSearchFallsBackToTrigramOnTypo(t *testing.T) {
	t.Parallel()

	index := &fakeIndex{
		fullText: nil,
		trigram: []search.Item{
			item(1, "Хачапури по-аджарски", 0.4, true),
			item(2, "Хачапури-пеновани", 0.35, true),
		},
	}

	service := search.NewService(index, openVenues{})

	page, err := service.Search(t.Context(), search.Query{Text: "хачипури", Limit: 10})
	require.NoError(t, err)

	assert.Equal(t, 1, index.trigramHits, "пустой полнотекст — повод добрать по опечатке")
	assert.Len(t, page.Items, 2)
}

func TestSearchMergesWithoutDuplicates(t *testing.T) {
	t.Parallel()

	shared := item(1, "Хачапури по-аджарски", 0.4, true)

	index := &fakeIndex{
		fullText: []search.Item{shared},
		trigram:  []search.Item{shared, item(2, "Хачапури-пеновани", 0.3, true)},
	}

	service := search.NewService(index, openVenues{})

	page, err := service.Search(t.Context(), search.Query{Text: "хачапури", Limit: 10})
	require.NoError(t, err)

	require.Len(t, page.Items, 2, "позиция, найденная обоими способами, не задваивается")
}

func TestSearchOnlyAvailable(t *testing.T) {
	t.Parallel()

	index := &fakeIndex{fullText: []search.Item{
		item(1, "Хачапури по-аджарски", 0.5, true),
		item(2, "Хинкали с бараниной", 0.5, false),
		item(3, "Кубдари", 0.4, true),
		item(4, "Лобио", 0.3, true),
		item(5, "Пхали", 0.2, true),
	}}

	service := search.NewService(index, openVenues{})

	page, err := service.Search(t.Context(), search.Query{Text: "меню", OnlyAvailable: true, Limit: 10})
	require.NoError(t, err)

	for i := range page.Items {
		assert.True(t, page.Items[i].IsAvailable)
	}

	assert.Len(t, page.Items, 4)
}

func TestSearchClosedVenueSinksButStays(t *testing.T) {
	t.Parallel()

	closedVenue := item(1, "Хачапури по-аджарски", 0.5, true)
	closedVenue.VenueID = venueID(2)

	index := &fakeIndex{fullText: []search.Item{
		closedVenue,
		item(2, "Хачапури по-мегрельски", 0.5, true),
		item(3, "Кубдари", 0.4, true),
		item(4, "Лобио", 0.3, true),
		item(5, "Пхали", 0.2, true),
	}}

	service := search.NewService(index, openVenues{closed: map[uuid.UUID]bool{venueID(2): true}})

	page, err := service.Search(t.Context(), search.Query{Text: "хачапури", Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 5)

	assert.Equal(t, "Хачапури по-мегрельски", page.Items[0].ItemName,
		"открытое заведение при равной релевантности выше закрытого")

	last := page.Items[len(page.Items)-1]
	assert.Equal(t, "Хачапури по-аджарски", last.ItemName,
		"блюдо закрытого заведения опускается вниз выдачи")
	assert.False(t, last.VenueIsOpenNow)
}

func TestSearchDistanceAffectsOrder(t *testing.T) {
	t.Parallel()

	far := item(1, "Хачапури по-аджарски", 0.5, true)
	far.Lat, far.Lon = 55.7998, 37.5350

	near := item(2, "Хачапури по-мегрельски", 0.45, true)

	index := &fakeIndex{fullText: []search.Item{
		far, near,
		item(3, "Кубдари", 0.2, true),
		item(4, "Лобио", 0.2, true),
		item(5, "Пхали", 0.2, true),
	}}

	service := search.NewService(index, openVenues{})
	point := catalog.Point{Lat: 55.7402, Lon: 37.6280}

	page, err := service.Search(t.Context(), search.Query{Text: "хачапури", Point: &point, Limit: 10})
	require.NoError(t, err)

	assert.Equal(t, "Хачапури по-мегрельски", page.Items[0].ItemName,
		"близкое блюдо обгоняет чуть более релевантное, но дальнее")
	require.NotNil(t, page.Items[0].DistanceM)
	assert.Zero(t, *page.Items[0].DistanceM)
}

func TestSearchPaginates(t *testing.T) {
	t.Parallel()

	index := &fakeIndex{fullText: []search.Item{
		item(1, "Первое", 0.9, true),
		item(2, "Второе", 0.8, true),
		item(3, "Третье", 0.7, true),
		item(4, "Четвёртое", 0.6, true),
		item(5, "Пятое", 0.5, true),
	}}

	service := search.NewService(index, openVenues{})

	first, err := service.Search(t.Context(), search.Query{Text: "меню", Limit: 2})
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.NextCursor)

	second, err := service.Search(t.Context(), search.Query{Text: "меню", Limit: 2, Cursor: first.NextCursor})
	require.NoError(t, err)
	require.Len(t, second.Items, 2)

	assert.NotEqual(t, first.Items[0].ItemID, second.Items[0].ItemID)
}
