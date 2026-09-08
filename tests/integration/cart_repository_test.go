package integration_test

import (
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/cart"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	otherVenueItem = uuid.MustParse("01890000-0003-7000-8000-000000000302") // tn-002, «Тонэ»
	hachapuriItem  = uuid.MustParse("01890000-0003-7000-8000-000000000101") // hv-001
	sizeLarge      = uuid.MustParse("01890000-0005-7000-8000-000000000102")
	sizeMedium     = uuid.MustParse("01890000-0005-7000-8000-000000000101")
)

// cartUser заводит отдельного пользователя на тест: корзина одна на человека,
// и тесты, делящие демо-юзера, мешали бы друг другу.
func cartUser(t *testing.T) uuid.UUID {
	t.Helper()

	ctx := testContext(t)
	id := uuid.New()

	_, err := pool.Exec(ctx, `INSERT INTO users (id, phone, name) VALUES ($1, $2, $3)`,
		id, "+7999"+id.String()[:7], "Тестовый покупатель")
	require.NoError(t, err)

	return id
}

func menuItem(t *testing.T, repo *cart.Repository, id uuid.UUID) cart.MenuItem {
	t.Helper()

	item, err := repo.MenuItemForAdd(testContext(t), id)
	require.NoError(t, err)

	return item
}

// Одинаковая позиция с тем же набором модификаторов увеличивает количество,
// а не плодит строки: две строки «Хинкали без добавок» — ошибка интерфейса,
// а не выбор пользователя.
func TestCartAddLineMergesSameLine(t *testing.T) {
	ctx := testContext(t)
	repo := cart.NewRepository(pool)
	user := cartUser(t)
	item := menuItem(t, repo, demoItem)

	require.NoError(t, repo.AddLine(ctx, user, item, nil, 2, item.PriceMinor))
	require.NoError(t, repo.AddLine(ctx, user, item, nil, 3, item.PriceMinor))

	current, err := repo.Get(ctx, user)
	require.NoError(t, err)

	require.Len(t, current.Lines, 1, "одинаковые позиции обязаны слиться")
	assert.Equal(t, 5, current.Lines[0].Qty)
	require.NotNil(t, current.VenueID)
	assert.Equal(t, demoVenue, *current.VenueID)
}

// Разный набор модификаторов — разные строки: «большая» и «обычная» —
// это два разных блюда для кухни и для чека.
func TestCartAddLineKeepsDifferentOptionsApart(t *testing.T) {
	ctx := testContext(t)
	repo := cart.NewRepository(pool)
	user := cartUser(t)
	item := menuItem(t, repo, hachapuriItem)

	require.NoError(t, repo.AddLine(ctx, user, item, []uuid.UUID{sizeMedium}, 1, item.PriceMinor))
	require.NoError(t, repo.AddLine(ctx, user, item, []uuid.UUID{sizeLarge}, 1, item.PriceMinor))

	current, err := repo.Get(ctx, user)
	require.NoError(t, err)
	assert.Len(t, current.Lines, 2)
}

// Корзина принадлежит одному заведению: позиция чужого заведения должна
// упереться в ошибку, а не молча смешать заказ.
func TestCartRejectsItemFromAnotherVenue(t *testing.T) {
	ctx := testContext(t)
	repo := cart.NewRepository(pool)
	user := cartUser(t)

	own := menuItem(t, repo, demoItem)
	require.NoError(t, repo.AddLine(ctx, user, own, nil, 1, own.PriceMinor))

	foreign := menuItem(t, repo, otherVenueItem)
	err := repo.AddLine(ctx, user, foreign, nil, 1, foreign.PriceMinor)

	require.ErrorIs(t, err, cart.ErrMixedVenues)

	var mixed cart.MixedVenuesError
	require.ErrorAs(t, err, &mixed)
	assert.Equal(t, "Хачапури и вино", mixed.VenueName,
		"ошибка обязана назвать заведение, которое занимает корзину")
}

// Цена запоминается на момент добавления: разница с текущей и есть
// сигнал PRICE_CHANGED (сценарий U7).
func TestCartKeepsPriceFromMomentOfAdding(t *testing.T) {
	ctx := testContext(t)
	repo := cart.NewRepository(pool)
	user := cartUser(t)
	item := menuItem(t, repo, demoItem)

	require.NoError(t, repo.AddLine(ctx, user, item, nil, 1, item.PriceMinor))

	_, err := pool.Exec(ctx,
		`UPDATE menu_items SET price_minor = price_minor + 5000 WHERE id = $1`, demoItem)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, restoreErr := pool.Exec(testContext(t),
			`UPDATE menu_items SET price_minor = price_minor - 5000 WHERE id = $1`, demoItem)
		require.NoError(t, restoreErr)
	})

	current, err := repo.Get(ctx, user)
	require.NoError(t, err)

	require.Len(t, current.Lines, 1)
	assert.Equal(t, item.PriceMinor, current.Lines[0].AddedUnitPriceMinor)
	assert.Equal(t, item.PriceMinor+5000, current.Lines[0].UnitPriceMinor)
}

// Опустевшая корзина отвязывается от заведения: иначе следующая попытка
// заказать в другом месте упиралась бы в «корзина занята» без единой позиции.
func TestCartDetachesVenueWhenLastLineRemoved(t *testing.T) {
	ctx := testContext(t)
	repo := cart.NewRepository(pool)
	user := cartUser(t)
	item := menuItem(t, repo, demoItem)

	require.NoError(t, repo.AddLine(ctx, user, item, nil, 1, item.PriceMinor))

	current, err := repo.Get(ctx, user)
	require.NoError(t, err)
	require.Len(t, current.Lines, 1)

	require.NoError(t, repo.DeleteLine(ctx, user, current.Lines[0].ID))

	empty, err := repo.Get(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, empty.Lines)
	assert.Nil(t, empty.VenueID, "пустая корзина не должна держать заведение")

	foreign := menuItem(t, repo, otherVenueItem)
	require.NoError(t, repo.AddLine(ctx, user, foreign, nil, 1, foreign.PriceMinor),
		"после опустошения можно заказать в другом заведении")
}

// Строку чужой корзины трогать нельзя, и это 404, а не 403.
func TestCartLineOperationsAreScopedToOwner(t *testing.T) {
	ctx := testContext(t)
	repo := cart.NewRepository(pool)
	owner, stranger := cartUser(t), cartUser(t)
	item := menuItem(t, repo, demoItem)

	require.NoError(t, repo.AddLine(ctx, owner, item, nil, 1, item.PriceMinor))

	current, err := repo.Get(ctx, owner)
	require.NoError(t, err)
	require.Len(t, current.Lines, 1)

	line := current.Lines[0].ID

	require.ErrorIs(t, repo.UpdateQty(ctx, stranger, line, 5), cart.ErrCartItemNotFound)
	require.ErrorIs(t, repo.DeleteLine(ctx, stranger, line), cart.ErrCartItemNotFound)

	require.NoError(t, repo.UpdateQty(ctx, owner, line, 5))

	updated, err := repo.Get(ctx, owner)
	require.NoError(t, err)
	assert.Equal(t, 5, updated.Lines[0].Qty)
}

// Clear чистит корзину целиком и тоже снимает привязку к заведению.
func TestCartClearEmptiesCart(t *testing.T) {
	ctx := testContext(t)
	repo := cart.NewRepository(pool)
	user := cartUser(t)
	item := menuItem(t, repo, demoItem)

	require.NoError(t, repo.AddLine(ctx, user, item, nil, 2, item.PriceMinor))
	require.NoError(t, repo.Clear(ctx, user))

	current, err := repo.Get(ctx, user)
	require.NoError(t, err)
	assert.True(t, current.IsEmpty())
	assert.Nil(t, current.VenueID)
}

// Позиция в стоп-листе в корзину не добавляется: каталог её уже не показывает.
func TestCartMenuItemForAddSkipsUnavailable(t *testing.T) {
	ctx := testContext(t)
	repo := cart.NewRepository(pool)

	_, err := repo.MenuItemForAdd(ctx, uuid.New())
	require.ErrorIs(t, err, cart.ErrMenuItemNotFound)

	item, err := repo.MenuItemForAdd(ctx, hachapuriItem)
	require.NoError(t, err)
	require.NotEmpty(t, item.Groups, "модификаторы позиции обязаны приехать вместе с ней")
	assert.Equal(t, 1, item.Groups[0].MinChoices, "группа «Размер» обязательна")
}
