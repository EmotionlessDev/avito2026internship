package integration_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/order"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Идентификаторы демо-данных: сид фиксирует их намеренно, чтобы тесты
// ссылались на значения, а не выбирали «какое-нибудь» заведение.
var (
	demoUser   = uuid.MustParse("01890000-0000-7000-8000-000000000001")
	demoUser2  = uuid.MustParse("01890000-0000-7000-8000-000000000002")
	demoVenue  = uuid.MustParse("01890000-0001-7000-8000-000000000001")
	otherVenue = uuid.MustParse("01890000-0001-7000-8000-000000000003")
	demoItem   = uuid.MustParse("01890000-0003-7000-8000-000000000104") // hv-004
)

func orderDraft(key string) order.Draft {
	return order.Draft{
		UserID:           demoUser,
		VenueID:          demoVenue,
		VenueTimezone:    "Europe/Moscow",
		ItemsAmountMinor: 117000,
		DeliveryFeeMinor: 14900,
		TotalAmountMinor: 131900,
		DeliveryAddress:  "Москва, Пятницкая улица, 20",
		DeliveryPoint:    order.Point{Lat: 55.7361, Lon: 37.6252},
		ContactPhone:     "+79990000001",
		IdempotencyKey:   key,
		Fingerprint:      "fingerprint-" + key,
		VenueName:        "Хачапури и вино",
		Items: []order.DraftItem{{
			MenuItemID: demoItem,
			Name:       "Хинкали с бараниной",
			PriceMinor: 39000,
			Qty:        3,
			TotalMinor: 117000,
		}},
	}
}

// Заказ, снимок состава, первая запись истории и событие в outbox обязаны
// появиться одной транзакцией: иначе бывает момент, когда заказ уже есть,
// а факта о нём никто не получил.
func TestOrderCreateWritesOrderItemsHistoryAndOutbox(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	created, isNew, err := repo.Create(ctx, orderDraft(uuid.NewString()))
	require.NoError(t, err)
	require.True(t, isNew)

	assert.Equal(t, order.StatusCreated, created.Status)
	assert.NotEmpty(t, created.Number, "человекочитаемый номер обязателен")
	assert.Equal(t, int64(131900), created.TotalAmountMinor)

	var items, history, events int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM order_items WHERE order_id = $1),
		       (SELECT count(*) FROM order_status_history WHERE order_id = $1),
		       (SELECT count(*) FROM outbox WHERE aggregate_id = $1)`, created.ID).
		Scan(&items, &history, &events))

	assert.Equal(t, 1, items, "состав заказа не записан")
	assert.Equal(t, 1, history, "первый переход не попал в историю")
	assert.Equal(t, 1, events, "событие order.created не попало в outbox")

	// Артикул заведения копируется в снимок: POS должен получить заказ
	// в своих идентификаторах, даже если позицию потом удалят из меню.
	var external string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT external_item_id FROM order_items WHERE order_id = $1`, created.ID).Scan(&external))
	assert.Equal(t, "hv-004", external)
}

// Повтор с тем же ключом идемпотентности обязан вернуть тот же заказ,
// а не создать второй: ретрай сети — норма, а не ошибка клиента.
func TestOrderCreateIsIdempotentByKey(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)
	draft := orderDraft(uuid.NewString())

	first, isNew, err := repo.Create(ctx, draft)
	require.NoError(t, err)
	require.True(t, isNew)

	second, isNew, err := repo.Create(ctx, draft)
	require.NoError(t, err)

	assert.False(t, isNew, "повтор не создаёт заказ")
	assert.Equal(t, first.ID, second.ID)

	var orders int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE user_id = $1 AND idempotency_key = $2`,
		demoUser, draft.IdempotencyKey).Scan(&orders))
	assert.Equal(t, 1, orders)
}

// Отпечаток запроса хранится вместе с заказом и читается по ключу:
// на нём сервис отличает честный ретрай от чужого запроса с тем же ключом
// и отвечает на второй 409 (проверка самого правила — в юнит-тестах сервиса).
func TestOrderByIdempotencyKeyReturnsStoredFingerprint(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	draft := orderDraft(uuid.NewString())
	created, _, err := repo.Create(ctx, draft)
	require.NoError(t, err)
	require.Equal(t, draft.Fingerprint, created.Fingerprint)

	found, ok, err := repo.ByIdempotencyKey(ctx, demoUser, draft.IdempotencyKey)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, created.ID, found.ID)
	assert.Equal(t, draft.Fingerprint, found.Fingerprint,
		"по отпечатку сервис отличит ретрай от чужого запроса с тем же ключом")

	_, ok, err = repo.ByIdempotencyKey(ctx, demoUser2, draft.IdempotencyKey)
	require.NoError(t, err)
	assert.False(t, ok, "ключ ищется в пределах пользователя")
}

// Один ключ у разных пользователей — разные заказы: уникальность составная,
// клиенты не обязаны согласовывать между собой генерацию ключей.
func TestOrderIdempotencyKeyIsScopedToUser(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	key := uuid.NewString()

	first, _, err := repo.Create(ctx, orderDraft(key))
	require.NoError(t, err)

	second := orderDraft(key)
	second.UserID = demoUser2

	other, isNew, err := repo.Create(ctx, second)
	require.NoError(t, err)

	assert.True(t, isNew)
	assert.NotEqual(t, first.ID, other.ID)
}

// Переход проверяется по текущему статусу из базы под блокировкой строки,
// поэтому недопустимый переход не проходит даже в обход сервиса.
func TestOrderApplyStatusChecksTransitionAndWritesEvent(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	created, _, err := repo.Create(ctx, orderDraft(uuid.NewString()))
	require.NoError(t, err)

	_, err = repo.ApplyStatus(ctx, order.Transition{
		OrderID: created.ID, To: order.StatusPlaced, Actor: order.ActorSystem,
	})
	require.NoError(t, err)

	// created → completed матрицей не разрешён.
	_, err = repo.ApplyStatus(ctx, order.Transition{
		OrderID: created.ID, To: order.StatusCompleted, Actor: order.ActorSystem,
	})
	require.Error(t, err)

	var invalid order.ErrInvalidTransition
	require.ErrorAs(t, err, &invalid)

	status, err := repo.StatusOf(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, order.StatusPlaced, status, "неудачный переход не должен менять статус")

	var events int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1 AND event_type = 'order.status_changed'`,
		created.ID).Scan(&events))
	assert.Equal(t, 1, events, "событие пишется только на состоявшийся переход")
}

// Повторный перевод в тот же статус — не ошибка: активность Temporal может
// выполниться дважды, и падать на этом нельзя.
func TestOrderApplyStatusIsIdempotent(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	created, _, err := repo.Create(ctx, orderDraft(uuid.NewString()))
	require.NoError(t, err)

	for range 2 {
		_, err = repo.ApplyStatus(ctx, order.Transition{
			OrderID: created.ID, To: order.StatusPlaced, Actor: order.ActorSystem,
		})
		require.NoError(t, err)
	}

	var history int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM order_status_history WHERE order_id = $1 AND to_status = 'placed'`,
		created.ID).Scan(&history))
	assert.Equal(t, 1, history, "повтор не должен плодить записи истории")
}

// Заказ чужого пользователя и чужого заведения — 404, а не 403: существование
// чужого ресурса тоже информация.
func TestOrderReadsAreScopedToOwner(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	created, _, err := repo.Create(ctx, orderDraft(uuid.NewString()))
	require.NoError(t, err)

	_, err = repo.ByUser(ctx, demoUser, created.ID)
	require.NoError(t, err)

	_, err = repo.ByUser(ctx, demoUser2, created.ID)
	require.ErrorIs(t, err, order.ErrNotFound)

	_, err = repo.ByVenue(ctx, demoVenue, created.ID)
	require.NoError(t, err)

	_, err = repo.ByVenue(ctx, otherVenue, created.ID)
	require.ErrorIs(t, err, order.ErrNotFound)
}

// Карточка заказа отдаёт состав и таймлайн: их собирают отдельные запросы,
// и забытый вызов виден только на настоящей базе.
func TestOrderByUserReturnsItemsAndTimeline(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	created, _, err := repo.Create(ctx, orderDraft(uuid.NewString()))
	require.NoError(t, err)

	_, err = repo.ApplyStatus(ctx, order.Transition{
		OrderID: created.ID, To: order.StatusPlaced, Actor: order.ActorSystem,
	})
	require.NoError(t, err)

	found, err := repo.ByUser(ctx, demoUser, created.ID)
	require.NoError(t, err)

	require.Len(t, found.Items, 1)
	assert.Equal(t, "Хинкали с бараниной", found.Items[0].Name)
	assert.Equal(t, int64(117000), found.Items[0].TotalMinor)

	require.Len(t, found.Timeline, 2)
	assert.Equal(t, order.StatusCreated, found.Timeline[0].ToStatus)
	assert.Equal(t, order.StatusPlaced, found.Timeline[1].ToStatus)
	assert.NotNil(t, found.PlacedAt, "placed_at заполняется на переходе")
}

// Холд оплаты — одна строка на заказ: повторная запись обновляет её,
// а не создаёт вторую.
func TestOrderPaymentIsSinglePerOrder(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	created, _, err := repo.Create(ctx, orderDraft(uuid.NewString()))
	require.NoError(t, err)

	require.NoError(t, repo.SavePayment(ctx, created.ID, "authorized", 131900, "hold-1"))
	require.NoError(t, repo.SavePayment(ctx, created.ID, "voided", 131900, "hold-1"))

	ref, err := repo.PaymentRef(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "hold-1", ref)

	var status string
	var payments int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*), max(status) FROM payments WHERE order_id = $1`, created.ID).
		Scan(&payments, &status))
	assert.Equal(t, 1, payments)
	assert.Equal(t, "voided", status)
}

// Лента заказов фильтруется по статусам и режется лимитом: поллинг-фолбэк
// заведения (сценарий M6) спрашивает ровно «что у меня новое».
func TestOrderListFiltersByStatusAndLimit(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	var placed, rejected order.Order

	for i := range 3 {
		created, _, err := repo.Create(ctx, orderDraft(uuid.NewString()))
		require.NoError(t, err)

		moved, err := repo.ApplyStatus(ctx, order.Transition{
			OrderID: created.ID, To: order.StatusPlaced, Actor: order.ActorSystem,
		})
		require.NoError(t, err)

		placed = moved

		if i == 0 {
			rejected, err = repo.ApplyStatus(ctx, order.Transition{
				OrderID: created.ID, To: order.StatusRejected, Actor: order.ActorVenue,
				Reason: "закончились продукты",
			})
			require.NoError(t, err)
		}
	}

	page, err := repo.ListByVenue(ctx, demoVenue, order.Filter{
		Statuses: []order.Status{order.StatusPlaced}, Limit: 50,
	})
	require.NoError(t, err)
	require.NotEmpty(t, page)

	for _, found := range page {
		assert.Equal(t, order.StatusPlaced, found.Status, "фильтр по статусу обязан работать")
	}

	assert.Contains(t, ids(page), placed.ID)
	assert.NotContains(t, ids(page), rejected.ID)

	limited, err := repo.ListByVenue(ctx, demoVenue, order.Filter{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited, 1, "лимит обязан резать выдачу")

	// Смещение двигает окно: вторая страница начинается с другого заказа.
	second, err := repo.ListByVenue(ctx, demoVenue, order.Filter{Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.NotEqual(t, limited[0].ID, second[0].ID)

	// История пользователя приезжает вместе с составом заказа.
	mine, err := repo.ListByUser(ctx, demoUser, order.Filter{Limit: 5})
	require.NoError(t, err)
	require.NotEmpty(t, mine)
	assert.NotEmpty(t, mine[0].Items, "лента без состава бесполезна витрине")

	// Чужая лента пуста, а не полна чужих заказов.
	foreign, err := repo.ListByVenue(ctx, otherVenue, order.Filter{Limit: 50})
	require.NoError(t, err)
	assert.Empty(t, foreign)
}

// Вебхук заведения читается активностью доставки заказа; незарегистрированный
// вебхук — не ошибка, а штатный путь на поллинг-фолбэк.
func TestOrderVenueWebhook(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	empty, err := repo.VenueWebhook(ctx, demoVenue)
	require.NoError(t, err)
	assert.Empty(t, empty.URL, "вебхука нет — активность уйдёт на поллинг")

	_, err = pool.Exec(ctx, `
		INSERT INTO venue_webhooks (venue_id, url, secret, is_active)
		VALUES ($1, $2, $3, true)`, demoVenue, "http://pos.local/hooks/orders", "секрет")
	require.NoError(t, err)

	t.Cleanup(func() {
		_, deleteErr := pool.Exec(testContext(t),
			`DELETE FROM venue_webhooks WHERE venue_id = $1`, demoVenue)
		require.NoError(t, deleteErr)
	})

	found, err := repo.VenueWebhook(ctx, demoVenue)
	require.NoError(t, err)
	assert.Equal(t, "http://pos.local/hooks/orders", found.URL)
	assert.Equal(t, "секрет", found.Secret)
}

func ids(orders []order.Order) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(orders))
	for i := range orders {
		out = append(out, orders[i].ID)
	}

	return out
}

// Двойной клик и параллельные ретраи сети приходят одновременно: заказ обязан
// получиться один, и каждый из запросов обязан получить именно его, а не 500
// от уникального индекса. Проверка ключа и вставка — check-then-act, и без
// обработки конфликта проигравший получал бы «duplicate key».
func TestOrderCreateSurvivesConcurrentSameKey(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)
	draft := orderDraft(uuid.NewString())

	const attempts = 8

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ids     []uuid.UUID
		created int
		errs    []error
	)

	wg.Add(attempts)

	for range attempts {
		go func() {
			defer wg.Done()

			got, isNew, err := repo.Create(ctx, draft)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err)

				return
			}

			ids = append(ids, got.ID)

			if isNew {
				created++
			}
		}()
	}

	wg.Wait()

	require.Empty(t, errs, "параллельный ретрай не должен падать ошибкой")
	require.Len(t, ids, attempts)
	assert.Equal(t, 1, created, "заказ создаётся ровно один раз")

	for _, id := range ids {
		assert.Equal(t, ids[0], id, "все запросы обязаны вернуть один и тот же заказ")
	}

	var stored int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE user_id = $1 AND idempotency_key = $2`,
		draft.UserID, draft.IdempotencyKey).Scan(&stored))
	assert.Equal(t, 1, stored)
}

// Поллинг-фолбэк заведения спрашивает «что изменилось с прошлого раза»:
// без этого фильтра POS на каждом тике перечитывал бы всю ленту (сценарий M6).
func TestOrderListFiltersByUpdatedSince(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	created, _, err := repo.Create(ctx, orderDraft(uuid.NewString()))
	require.NoError(t, err)

	// Отсечки берём из базы, а не с часов хоста: они расходятся на доли
	// миллисекунды, и заказ, изменённый сразу после отсечки, оказывался бы
	// то внутри окна, то снаружи.
	future := databaseNow(t).Add(time.Hour)

	nothing, err := repo.ListByVenue(ctx, demoVenue, order.Filter{
		UpdatedSince: &future, Limit: 50,
	})
	require.NoError(t, err)
	assert.Empty(t, nothing, "заказов, изменённых позже, чем через час, быть не может")

	past := databaseNow(t).Add(-time.Hour)

	page, err := repo.ListByVenue(ctx, demoVenue, order.Filter{
		UpdatedSince: &past, Limit: 50,
	})
	require.NoError(t, err)
	assert.Contains(t, ids(page), created.ID)

	// Переход двигает updated_at, поэтому заказ снова попадает в окно.
	marker := databaseNow(t)

	_, err = repo.ApplyStatus(ctx, order.Transition{
		OrderID: created.ID, To: order.StatusPlaced, Actor: order.ActorSystem,
	})
	require.NoError(t, err)

	changed, err := repo.ListByVenue(ctx, demoVenue, order.Filter{
		UpdatedSince: &marker, Limit: 50,
	})
	require.NoError(t, err)
	assert.Contains(t, ids(changed), created.ID, "изменённый заказ обязан попасть в выборку")
}

// databaseNow отдаёт время глазами Postgres: сравнивать его отметки
// с часами хоста нельзя, они расходятся.
func databaseNow(t *testing.T) time.Time {
	t.Helper()

	var now time.Time
	require.NoError(t, pool.QueryRow(testContext(t), `SELECT now()`).Scan(&now))

	return now
}

// Гонка не должна прятать ошибку клиента: если два одновременных запроса
// с одним ключом несут разные тела, второй обязан получить 409, а не чужой
// заказ. Проверка отпечатка до коммита соседа тут не срабатывает — оба
// запроса проходят её, пока ни один ещё не записан.
func TestOrderCreateConflictSurvivesConcurrentDifferentBody(t *testing.T) {
	ctx := testContext(t)
	repo := order.NewRepository(pool)

	key := uuid.NewString()

	first := orderDraft(key)
	first.Fingerprint = "отпечаток-первого"
	first.DeliveryAddress = "Москва, Пятницкая улица, 20"

	second := orderDraft(key)
	second.Fingerprint = "отпечаток-второго"
	second.DeliveryAddress = "Москва, другой адрес"

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded []order.Order
		conflicts int
		other     []error
	)

	wg.Add(2)

	for _, draft := range []order.Draft{first, second} {
		go func() {
			defer wg.Done()

			got, _, err := repo.Create(ctx, draft)

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				succeeded = append(succeeded, got)
			case errors.Is(err, order.ErrIdempotencyConflict):
				conflicts++
			default:
				other = append(other, err)
			}
		}()
	}

	wg.Wait()

	require.Empty(t, other, "других ошибок быть не должно")

	var stored int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE user_id = $1 AND idempotency_key = $2`,
		first.UserID, key).Scan(&stored))
	assert.Equal(t, 1, stored, "заказ по ключу всегда один")

	// Гонка могла разрешиться и последовательно — тогда второй запрос
	// упёрся в проверку отпечатка ещё до вставки. Важно одно: чужое тело
	// никогда не получает чужой заказ.
	require.Len(t, succeeded, 1, "успешным может быть только один запрос")
	assert.Equal(t, 1, conflicts, "второй обязан получить конфликт ключа")
}
