package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/merchant"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ключи из сида. Открытые значения лежат в scripts/seed/data.sql: в базе
// хранится только их sha256.
const (
	hachapuriKey = "kitchen_demo_hachapuri_key"
	toneKey      = "kitchen_demo_tone_key"
)

// tenantContext кладёт заведение в контекст так же, как это делает middleware
// аутентификации. Репозиторий берёт venue_id только оттуда — обойти нельзя.
func tenantContext(t *testing.T, venueID uuid.UUID) context.Context {
	t.Helper()

	return merchant.WithTenant(testContext(t), venueID)
}

// Ключ ищется по хэшу: открытого значения в базе нет, отозванный не работает.
func TestMerchantAuthenticateKey(t *testing.T) {
	ctx := testContext(t)
	repo := merchant.NewRepository(pool)

	venueID, err := repo.AuthenticateKey(ctx, merchant.HashAPIKey(hachapuriKey))
	require.NoError(t, err)
	assert.Equal(t, demoVenue, venueID)

	_, err = repo.AuthenticateKey(ctx, merchant.HashAPIKey("kitchen_demo_wrong_key"))
	require.ErrorIs(t, err, merchant.ErrUnauthorized)

	_, err = pool.Exec(ctx,
		`UPDATE venue_api_keys SET revoked_at = now() WHERE venue_id = $1`, otherVenue)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, restoreErr := pool.Exec(testContext(t),
			`UPDATE venue_api_keys SET revoked_at = NULL WHERE venue_id = $1`, otherVenue)
		require.NoError(t, restoreErr)
	})

	_, err = repo.AuthenticateKey(ctx, merchant.HashAPIKey(toneKey))
	require.ErrorIs(t, err, merchant.ErrUnauthorized, "отозванный ключ не должен пускать")
}

// Заведение видит только своё меню: venue_id подставляет репозиторий,
// а не хендлер, поэтому забыть фильтр физически негде.
func TestMerchantCurrentMenuIsScopedToTenant(t *testing.T) {
	repo := merchant.NewRepository(pool)

	own, err := repo.CurrentMenu(tenantContext(t, demoVenue))
	require.NoError(t, err)
	assert.Len(t, own.Items, 9)
	assert.Contains(t, own.Items, "hv-001")
	assert.NotContains(t, own.Items, "tn-001", "в меню не должно быть чужих артикулов")

	foreign, err := repo.CurrentMenu(tenantContext(t, otherVenue))
	require.NoError(t, err)
	assert.Len(t, foreign.Items, 8)
	assert.Contains(t, foreign.Items, "tn-001")

	_, err = repo.CurrentMenu(testContext(t))
	require.ErrorIs(t, err, merchant.ErrNoTenant, "без арендатора в контексте чтения нет")
}

// Импорт заводит новую позицию, обновляет изменившуюся и мягко архивирует
// исчезнувшую: на архивированную ссылаются исторические заказы.
func TestMerchantApplyImportCreatesUpdatesAndArchives(t *testing.T) {
	ctx := tenantContext(t, otherVenue)
	repo := merchant.NewRepository(pool)

	current, err := repo.CurrentMenu(ctx)
	require.NoError(t, err)

	snapshot := merchant.Snapshot{
		Categories: []merchant.SnapshotCategory{{ExternalID: "tn-cat-test", Name: "Тест", Position: 1}},
		Items: []merchant.SnapshotItem{{
			ExternalID:         "tn-999",
			CategoryExternalID: "tn-cat-test",
			Name:               "Новая позиция",
			PriceMinor:         12345,
			IsAvailable:        true,
		}},
	}

	changes, err := merchant.Diff(current, snapshot, merchant.NewID)
	require.NoError(t, err)

	assert.Equal(t, 1, changes.Created)
	assert.Equal(t, 8, changes.Archived, "всё, чего нет в снапшоте, архивируется")

	hash, err := merchant.PayloadHash(snapshot)
	require.NoError(t, err)

	require.NoError(t, repo.ApplyImport(ctx, merchant.NewID(), hash, changes))

	t.Cleanup(func() {
		restoreCtx := testContext(t)
		statements := []string{
			`UPDATE menu_items SET is_archived = false WHERE venue_id = $1 AND external_id <> 'tn-999'`,
			`DELETE FROM menu_items WHERE venue_id = $1 AND external_id = 'tn-999'`,
			`DELETE FROM menu_categories WHERE venue_id = $1 AND external_id = 'tn-cat-test'`,
			`DELETE FROM menu_imports WHERE venue_id = $1`,
		}

		for _, statement := range statements {
			_, restoreErr := pool.Exec(restoreCtx, statement, otherVenue)
			require.NoError(t, restoreErr)
		}
	})

	// Отпечаток запомнен — повторный тот же снапшот работы не потребует.
	stored, err := repo.LastImportHash(ctx)
	require.NoError(t, err)
	assert.Equal(t, hash, stored)

	// CurrentMenu отдаёт и архивные позиции: вернувшийся артикул должен
	// разархивироваться, а не создаться вторым.
	after, err := repo.CurrentMenu(ctx)
	require.NoError(t, err)
	require.Len(t, after.Items, 9)

	assert.False(t, after.Items["tn-999"].IsArchived, "новая позиция активна")
	assert.True(t, after.Items["tn-001"].IsArchived, "исчезнувшая архивируется, а не удаляется")

	var alive int
	require.NoError(t, pool.QueryRow(testContext(t),
		`SELECT count(*) FROM menu_items WHERE venue_id = $1 AND NOT is_archived`, otherVenue).
		Scan(&alive))
	assert.Equal(t, 1, alive, "в продаже осталась одна позиция")
}

// Вернувшийся артикул разархивируется, а не создаётся заново: иначе
// исторические заказы ссылались бы на позицию-двойника.
func TestMerchantImportRestoresArchivedItem(t *testing.T) {
	ctx := tenantContext(t, otherVenue)
	repo := merchant.NewRepository(pool)

	_, err := pool.Exec(testContext(t),
		`UPDATE menu_items SET is_archived = true WHERE venue_id = $1 AND external_id = 'tn-008'`,
		otherVenue)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, restoreErr := pool.Exec(testContext(t),
			`UPDATE menu_items SET is_archived = false WHERE venue_id = $1`, otherVenue)
		require.NoError(t, restoreErr)

		_, restoreErr = pool.Exec(testContext(t),
			`DELETE FROM menu_imports WHERE venue_id = $1`, otherVenue)
		require.NoError(t, restoreErr)
	})

	var idBefore uuid.UUID
	require.NoError(t, pool.QueryRow(testContext(t),
		`SELECT id FROM menu_items WHERE venue_id = $1 AND external_id = 'tn-008'`, otherVenue).
		Scan(&idBefore))

	current, err := repo.CurrentMenu(ctx)
	require.NoError(t, err)

	snapshot := merchant.Snapshot{
		Categories: []merchant.SnapshotCategory{{ExternalID: "cat-drinks", Name: "Напитки", Position: 3}},
		Items: []merchant.SnapshotItem{{
			ExternalID:         "tn-008",
			CategoryExternalID: "cat-drinks",
			Name:               "Мацони",
			PriceMinor:         14000,
			IsAvailable:        true,
		}},
	}

	changes, err := merchant.Diff(current, snapshot, merchant.NewID)
	require.NoError(t, err)

	hash, err := merchant.PayloadHash(snapshot)
	require.NoError(t, err)
	require.NoError(t, repo.ApplyImport(ctx, merchant.NewID(), hash, changes))

	var idAfter uuid.UUID
	var archived bool
	require.NoError(t, pool.QueryRow(testContext(t),
		`SELECT id, is_archived FROM menu_items WHERE venue_id = $1 AND external_id = 'tn-008'`,
		otherVenue).Scan(&idAfter, &archived))

	assert.Equal(t, idBefore, idAfter, "идентификатор позиции обязан сохраниться")
	assert.False(t, archived)
}

// Стоп-лист переключает и позиции, и опции батчем, а неизвестные артикулы
// возвращает списком, а не молча игнорирует.
func TestMerchantSetAvailability(t *testing.T) {
	ctx := tenantContext(t, demoVenue)
	repo := merchant.NewRepository(pool)

	result, scheduled, err := repo.SetAvailability(ctx, merchant.AvailabilityRequest{
		Items: []merchant.AvailabilityChange{
			{ExternalID: "hv-004", IsAvailable: false},
			{ExternalID: "hv-нет-такого", IsAvailable: false},
		},
		Options: []merchant.AvailabilityChange{
			{ExternalID: "hv-001-size-l", IsAvailable: false},
		},
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _, restoreErr := repo.SetAvailability(tenantContext(t, demoVenue), merchant.AvailabilityRequest{
			Items:   []merchant.AvailabilityChange{{ExternalID: "hv-004", IsAvailable: true}},
			Options: []merchant.AvailabilityChange{{ExternalID: "hv-001-size-l", IsAvailable: true}},
		})
		require.NoError(t, restoreErr)
	})

	assert.Equal(t, 1, result.ItemsUpdated)
	assert.Equal(t, 1, result.OptionsUpdated)
	assert.Equal(t, []string{"hv-нет-такого"}, result.NotFound)
	assert.Empty(t, scheduled, "без until авто-возврат не планируется")

	var itemAvailable, optionAvailable bool
	require.NoError(t, pool.QueryRow(testContext(t), `
		SELECT (SELECT is_available FROM menu_items WHERE venue_id = $1 AND external_id = 'hv-004'),
		       (SELECT o.is_available FROM menu_options o
		          JOIN menu_option_groups g ON g.id = o.group_id
		          JOIN menu_items i ON i.id = g.item_id
		         WHERE i.venue_id = $1 AND o.external_id = 'hv-001-size-l')`, demoVenue).
		Scan(&itemAvailable, &optionAvailable))
	assert.False(t, itemAvailable)
	assert.False(t, optionAvailable)
}

// Чужой артикул недоступен даже по точному совпадению имени: фильтр
// по venue_id стоит в самом запросе.
func TestMerchantSetAvailabilityCannotTouchForeignVenue(t *testing.T) {
	repo := merchant.NewRepository(pool)

	result, _, err := repo.SetAvailability(tenantContext(t, otherVenue), merchant.AvailabilityRequest{
		Items: []merchant.AvailabilityChange{{ExternalID: "hv-004", IsAvailable: false}},
	})
	require.NoError(t, err)

	assert.Zero(t, result.ItemsUpdated)
	assert.Equal(t, []string{"hv-004"}, result.NotFound)

	var available bool
	require.NoError(t, pool.QueryRow(testContext(t),
		`SELECT is_available FROM menu_items WHERE id = $1`, demoItem).Scan(&available))
	assert.True(t, available, "чужой стоп-лист не должен трогать наше меню")
}

// PUT настроек заменяет переданные поля и не обнуляет остальные,
// а расписание заменяется целиком.
func TestMerchantUpdateVenueReplacesOnlyGivenFields(t *testing.T) {
	ctx := tenantContext(t, demoVenue)
	repo := merchant.NewRepository(pool)

	before, err := repo.VenueSettingsSnapshot(ctx)
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, repo.UpdateVenue(tenantContext(t, demoVenue), before))
	})

	accepting := false
	minOrder := int64(222000)

	require.NoError(t, repo.UpdateVenue(ctx, merchant.VenueSettings{
		IsAcceptingOrders:   &accepting,
		MinOrderAmountMinor: &minOrder,
		WorkHours: []merchant.WorkInterval{
			{Weekday: 0, OpensAt: "09:00", ClosesAt: "18:00"},
		},
	}))

	after, err := repo.VenueSettingsSnapshot(ctx)
	require.NoError(t, err)

	require.NotNil(t, after.IsAcceptingOrders)
	assert.False(t, *after.IsAcceptingOrders)
	assert.Equal(t, minOrder, *after.MinOrderAmountMinor)
	assert.Equal(t, *before.DeliveryFeeMinor, *after.DeliveryFeeMinor,
		"непереданное поле обязано сохраниться")
	require.Len(t, after.WorkHours, 1, "расписание заменяется целиком")
}

// Вебхук — одна запись на заведение: повторная регистрация обновляет её.
func TestMerchantUpsertWebhook(t *testing.T) {
	ctx := tenantContext(t, otherVenue)
	repo := merchant.NewRepository(pool)

	t.Cleanup(func() {
		_, err := pool.Exec(testContext(t), `DELETE FROM venue_webhooks WHERE venue_id = $1`, otherVenue)
		require.NoError(t, err)
	})

	saved, err := repo.UpsertWebhook(ctx, merchant.Webhook{
		URL: "http://pos.local/hooks/orders", Secret: "секрет-1",
	})
	require.NoError(t, err)
	assert.True(t, saved.IsActive)

	_, err = repo.UpsertWebhook(ctx, merchant.Webhook{
		URL: "http://pos.local/hooks/v2/orders", Secret: "секрет-2",
	})
	require.NoError(t, err)

	var count int
	var url string
	require.NoError(t, pool.QueryRow(testContext(t),
		`SELECT count(*), max(url) FROM venue_webhooks WHERE venue_id = $1`, otherVenue).
		Scan(&count, &url))
	assert.Equal(t, 1, count)
	assert.Equal(t, "http://pos.local/hooks/v2/orders", url)

	targets, err := repo.VenuesWithWebhook(testContext(t))
	require.NoError(t, err)
	assert.NotEmpty(t, targets, "сверка меню обходит заведения с вебхуком")
}

// День без заказов — это нули, а не 404: витрина обязана ответить.
func TestMerchantDailyStatsReturnsZeroesWhenNoOrders(t *testing.T) {
	repo := merchant.NewRepository(pool)

	stats, err := repo.DailyStats(tenantContext(t, otherVenue), nil)
	require.NoError(t, err)

	assert.Zero(t, stats.OrdersTotal)
	assert.Zero(t, stats.RevenueMinor)
	assert.Nil(t, stats.AvgAcceptSeconds, "подтверждений не было — среднего нет")
	assert.False(t, stats.Date.IsZero(), "день заведения обязан приехать")
}

// Контракт обещает выбор дня, и он обязан работать: без него заведение
// не посмотрит вчерашнюю выручку, а параметр в спеке был бы враньём.
func TestMerchantDailyStatsHonoursRequestedDay(t *testing.T) {
	ctx := tenantContext(t, demoVenue)
	repo := merchant.NewRepository(pool)

	past := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)

	_, err := pool.Exec(testContext(t), `
		INSERT INTO venue_daily_stats (venue_id, stat_date, orders_total, orders_completed, revenue_minor)
		VALUES ($1, $2, 7, 5, 555000)
		ON CONFLICT (venue_id, stat_date) DO UPDATE
		SET orders_total = EXCLUDED.orders_total,
		    orders_completed = EXCLUDED.orders_completed,
		    revenue_minor = EXCLUDED.revenue_minor`, demoVenue, past)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, deleteErr := pool.Exec(testContext(t),
			`DELETE FROM venue_daily_stats WHERE venue_id = $1 AND stat_date = $2`, demoVenue, past)
		require.NoError(t, deleteErr)
	})

	asked, err := repo.DailyStats(ctx, &past)
	require.NoError(t, err)

	assert.Equal(t, past.Format("2006-01-02"), asked.Date.Format("2006-01-02"))
	assert.Equal(t, 7, asked.OrdersTotal)
	assert.Equal(t, int64(555000), asked.RevenueMinor)

	// День без данных — нули, а не чужие цифры и не ошибка.
	empty := time.Date(2019, 5, 5, 0, 0, 0, 0, time.UTC)

	blank, err := repo.DailyStats(ctx, &empty)
	require.NoError(t, err)
	assert.Zero(t, blank.OrdersTotal)
	assert.Equal(t, empty.Format("2006-01-02"), blank.Date.Format("2006-01-02"))
}
