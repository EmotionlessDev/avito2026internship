package integration_test

import (
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/catalog"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Каталог отдаёт только опубликованные заведения: снятое с публикации
// не должно всплывать в выдаче, даже если на него ссылается чья-то корзина.
func TestCatalogListVenuesReturnsActiveOnly(t *testing.T) {
	ctx := testContext(t)
	repo := catalog.NewRepository(pool)

	venues, err := repo.ListVenues(ctx, "")
	require.NoError(t, err)
	require.Len(t, venues, 3)

	_, err = pool.Exec(ctx, `UPDATE venues SET status = 'suspended' WHERE id = $1`, otherVenue)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, restoreErr := pool.Exec(testContext(t),
			`UPDATE venues SET status = 'active' WHERE id = $1`, otherVenue)
		require.NoError(t, restoreErr)
	})

	active, err := repo.ListVenues(ctx, "")
	require.NoError(t, err)
	assert.Len(t, active, 2)

	_, err = repo.GetVenue(ctx, otherVenue)
	require.ErrorIs(t, err, catalog.ErrVenueNotFound)
}

// Поиск заведения по названию — обычный ILIKE, но с экранированием:
// проценты и подчёркивания в запросе не должны превращаться в шаблон.
func TestCatalogListVenuesFiltersByNameAndEscapesWildcards(t *testing.T) {
	ctx := testContext(t)
	repo := catalog.NewRepository(pool)

	found, err := repo.ListVenues(ctx, "хачапури")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "Хачапури и вино", found[0].Name)

	nothing, err := repo.ListVenues(ctx, "%")
	require.NoError(t, err)
	assert.Empty(t, nothing, "wildcard в запросе не должен возвращать всё подряд")
}

// Карточка заведения приезжает вместе с расписанием: без него нельзя
// посчитать «открыто сейчас».
func TestCatalogGetVenueLoadsSchedule(t *testing.T) {
	ctx := testContext(t)
	repo := catalog.NewRepository(pool)

	venue, err := repo.GetVenue(ctx, demoVenue)
	require.NoError(t, err)

	assert.Equal(t, "hachapuri-i-vino", venue.Slug)
	assert.Equal(t, "Europe/Moscow", venue.Timezone)
	assert.Equal(t, int64(100000), venue.MinOrderAmountMinor)
	assert.Len(t, venue.Schedule.Intervals, 7, "расписание на каждый день недели")

	_, err = repo.GetVenue(ctx, uuid.New())
	require.ErrorIs(t, err, catalog.ErrVenueNotFound)
}

// Меню по умолчанию прячет стоп-лист, а с флагом отдаёт его с is_available=false:
// витрина должна уметь показать «сегодня закончилось», а не прятать блюдо.
func TestCatalogGetMenuHidesStopListUnlessAsked(t *testing.T) {
	ctx := testContext(t)
	repo := catalog.NewRepository(pool)

	_, err := pool.Exec(ctx, `UPDATE menu_items SET is_available = false WHERE id = $1`, demoItem)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, restoreErr := pool.Exec(testContext(t),
			`UPDATE menu_items SET is_available = true WHERE id = $1`, demoItem)
		require.NoError(t, restoreErr)
	})

	visible, err := repo.GetMenu(ctx, demoVenue, false)
	require.NoError(t, err)
	assert.False(t, hasItem(visible, demoItem), "снятая позиция не должна показываться")

	full, err := repo.GetMenu(ctx, demoVenue, true)
	require.NoError(t, err)

	item, found := findItem(full, demoItem)
	require.True(t, found, "с include_unavailable позиция обязана прийти")
	assert.False(t, item.IsAvailable)
}

// Модификаторы приезжают вместе с позициями и в порядке показа: собирать
// их отдельным запросом на клиенте пришлось бы для каждой строки меню.
func TestCatalogGetMenuAttachesOptionGroups(t *testing.T) {
	ctx := testContext(t)
	repo := catalog.NewRepository(pool)

	menu, err := repo.GetMenu(ctx, demoVenue, false)
	require.NoError(t, err)

	item, found := findItem(menu, hachapuriItem)
	require.True(t, found)
	require.Len(t, item.OptionGroups, 2)

	assert.Equal(t, "Размер", item.OptionGroups[0].Name)
	assert.Equal(t, 1, item.OptionGroups[0].MinChoices)
	assert.Equal(t, "Добавки", item.OptionGroups[1].Name)
	assert.NotEmpty(t, item.OptionGroups[0].Options)
}

// Меню чужого заведения по его id не отдаётся вперемешку: позиции обязаны
// быть отфильтрованы по venue_id.
func TestCatalogGetMenuIsScopedToVenue(t *testing.T) {
	ctx := testContext(t)
	repo := catalog.NewRepository(pool)

	menu, err := repo.GetMenu(ctx, otherVenue, true)
	require.NoError(t, err)

	assert.Equal(t, otherVenue, menu.VenueID)
	assert.False(t, hasItem(menu, demoItem), "позиция соседнего заведения не должна попасть в меню")

	_, err = repo.GetMenu(ctx, uuid.New(), false)
	require.ErrorIs(t, err, catalog.ErrVenueNotFound)
}

// Расписания читаются пачкой на список заведений: иначе выдача каталога
// делала бы запрос на каждую карточку.
func TestCatalogSchedulesByVenueIDs(t *testing.T) {
	ctx := testContext(t)
	repo := catalog.NewRepository(pool)

	schedules, err := repo.SchedulesByVenueIDs(ctx, []uuid.UUID{demoVenue, otherVenue})
	require.NoError(t, err)

	require.Len(t, schedules, 2)
	assert.Len(t, schedules[demoVenue].Intervals, 7)
	assert.Len(t, schedules[otherVenue].Intervals, 7)

	empty, err := repo.SchedulesByVenueIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func findItem(menu catalog.Menu, id uuid.UUID) (catalog.Item, bool) {
	for _, category := range menu.Categories {
		for _, item := range category.Items {
			if item.ID == id {
				return item, true
			}
		}
	}

	return catalog.Item{}, false
}

func hasItem(menu catalog.Menu, id uuid.UUID) bool {
	_, found := findItem(menu, id)

	return found
}
