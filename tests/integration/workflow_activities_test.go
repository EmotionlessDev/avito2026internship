package integration_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/order"
	orderworkflow "github.com/emotionlessdev/avito-kitchen/internal/order/workflow"
	"github.com/emotionlessdev/avito-kitchen/internal/payments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// Активности проверяются на живой базе и живом HTTP: их работа — это ровно
// поход во внешний мир, и подмена обоих концов оставила бы от теста мок,
// проверяющий мок. Сам процесс проверяется отдельно, юнит-тестами workflow.
//
// Запускаются они через тестовое окружение Temporal, а не прямым вызовом:
// активность вправе рассчитывать на свой контекст — из него она берёт логгер
// и heartbeat, — и вызов в обход окружения падал бы паникой SDK.
type activityRunner struct {
	env        *testsuite.TestActivityEnvironment
	activities *orderworkflow.Activities
}

func newActivities(t *testing.T) activityRunner {
	t.Helper()

	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	activities := orderworkflow.NewActivities(order.NewRepository(pool), payments.NewMock())
	env.RegisterActivity(activities)

	return activityRunner{env: env, activities: activities}
}

// run выполняет активность и отдаёт её ошибку. Значение возврата тестам
// нужно редко, поэтому оно достаётся отдельным хелпером.
func (r activityRunner) run(t *testing.T, activity any, args ...any) error {
	t.Helper()

	_, err := r.env.ExecuteActivity(activity, args...)

	return err
}

func createdOrder(t *testing.T) order.Order {
	t.Helper()

	created, _, err := order.NewRepository(pool).Create(testContext(t), orderDraft(uuid.NewString()))
	require.NoError(t, err)

	return created
}

// Холд записывается в базу той же активностью, которая его получила:
// иначе состояние платежа осталось бы только у провайдера.
func TestActivityAuthorizeAndCapturePayment(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	require.NoError(t, runner.run(t, runner.activities.AuthorizePayment, orderworkflow.AuthorizeInput{
		OrderID: created.ID, AmountMinor: created.TotalAmountMinor,
	}))
	assert.Equal(t, "authorized", paymentStatus(t, created.ID))

	require.NoError(t, runner.run(t, runner.activities.CapturePayment, created.ID))
	assert.Equal(t, "captured", paymentStatus(t, created.ID))
}

// Компенсация возвращает холд и помечает платёж — это тот самый шаг,
// ради которого в процессе есть saga.
func TestActivityVoidPayment(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	require.NoError(t, runner.run(t, runner.activities.AuthorizePayment, orderworkflow.AuthorizeInput{
		OrderID: created.ID, AmountMinor: created.TotalAmountMinor,
	}))

	require.NoError(t, runner.run(t, runner.activities.VoidPayment, created.ID))
	assert.Equal(t, "voided", paymentStatus(t, created.ID))
}

// Холда не было — списывать и возвращать нечего, и это не ошибка:
// заказ мог дойти сюда после ручного вмешательства.
func TestActivityPaymentStepsAreNoopWithoutHold(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	require.NoError(t, runner.run(t, runner.activities.CapturePayment, created.ID))
	require.NoError(t, runner.run(t, runner.activities.VoidPayment, created.ID))

	var payments int
	require.NoError(t, pool.QueryRow(testContext(t),
		`SELECT count(*) FROM payments WHERE order_id = $1`, created.ID).Scan(&payments))
	assert.Zero(t, payments)
}

// Отказ провайдера — не общий сбой: он приходит отдельной ошибкой, которую
// Temporal не станет повторять. Мок отказывает детерминированно, на сумме
// с 99 копейками (сценарий U14).
func TestActivityAuthorizeDeclineIsNotRetried(t *testing.T) {
	runner := newActivities(t)

	draft := orderDraft(uuid.NewString())
	draft.TotalAmountMinor = 149999

	created, _, err := order.NewRepository(pool).Create(testContext(t), draft)
	require.NoError(t, err)

	err = runner.run(t, runner.activities.AuthorizePayment, orderworkflow.AuthorizeInput{
		OrderID: created.ID, AmountMinor: draft.TotalAmountMinor,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "отклонён")
}

// SetStatus — единственный, кто двигает orders.status. Недопустимый переход
// возвращает ошибку, которую Temporal не повторяет: состояние уже другое.
func TestActivitySetStatus(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	require.NoError(t, runner.run(t, runner.activities.SetStatus, orderworkflow.SetStatusInput{
		OrderID: created.ID, To: order.StatusPlaced, Actor: order.ActorSystem,
	}))

	eta := 35
	require.NoError(t, runner.run(t, runner.activities.SetStatus, orderworkflow.SetStatusInput{
		OrderID: created.ID, To: order.StatusAccepted, Actor: order.ActorVenue, ETAMinutes: &eta,
	}))

	stored, err := order.NewRepository(pool).ByID(testContext(t), created.ID)
	require.NoError(t, err)
	assert.Equal(t, order.StatusAccepted, stored.Status)
	require.NotNil(t, stored.ETAMinutes)
	assert.Equal(t, 35, *stored.ETAMinutes)

	err = runner.run(t, runner.activities.SetStatus, orderworkflow.SetStatusInput{
		OrderID: created.ID, To: order.StatusCompleted, Actor: order.ActorSystem,
	})
	require.Error(t, err, "переход accepted → completed матрицей не разрешён")
}

// Заказ уезжает в заведение подписанным вебхуком в его артикулах.
func TestActivitySendOrderToVenueSignsRequest(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	var (
		mu       sync.Mutex
		gotPath  string
		headers  http.Header
		bodySeen []byte
	)

	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		gotPath = r.URL.Path
		headers = r.Header.Clone()
		bodySeen = readAll(t, r)

		w.WriteHeader(http.StatusAccepted)
	}))
	defer venue.Close()

	registerWebhook(t, venue.URL+"/pos/webhooks/orders")

	require.NoError(t, runner.run(t, runner.activities.SendOrderToVenue, created.ID))

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, "/pos/webhooks/orders", gotPath)
	assert.Regexp(t, `^sha256=[0-9a-f]{64}$`, headers.Get("X-Kitchen-Signature"))
	assert.NotEmpty(t, headers.Get("X-Kitchen-Event-Id"))
	assert.NotEmpty(t, headers.Get("X-Kitchen-Timestamp"))
	assert.Contains(t, string(bodySeen), `"external_item_id":"hv-004"`,
		"заказ обязан уехать в артикулах заведения")
}

// Отказ заведения — ошибка активности: Temporal повторит вызов, а после
// исчерпания попыток заказ заберёт поллинг-фолбэк (сценарий M6).
func TestActivitySendOrderToVenueFailsOnBadStatus(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer venue.Close()

	registerWebhook(t, venue.URL+"/pos/webhooks/orders")

	err := runner.run(t, runner.activities.SendOrderToVenue, created.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
}

// Повторную доставку заведение опознаёт по event_id и отвечает 409.
// Для платформы это успех, а не повод повторять снова.
func TestActivitySendOrderToVenueTreatsConflictAsSuccess(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer venue.Close()

	registerWebhook(t, venue.URL+"/pos/webhooks/orders")

	require.NoError(t, runner.run(t, runner.activities.SendOrderToVenue, created.ID))
}

// Без зарегистрированного вебхука активность не падает: заказ ждёт поллинга.
func TestActivitySendOrderToVenueWithoutWebhook(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	require.NoError(t, runner.run(t, runner.activities.SendOrderToVenue, created.ID))
}

// Отменённый заказ снимается с производства отдельным вебхуком: без него
// кухня считала бы его живым (это и есть сценарий U10 со стороны заведения).
func TestActivityNotifyVenueCancelled(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	var (
		mu      sync.Mutex
		gotPath string
		body    []byte
	)

	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		gotPath = r.URL.Path
		body = readAll(t, r)

		w.WriteHeader(http.StatusAccepted)
	}))
	defer venue.Close()

	registerWebhook(t, venue.URL+"/pos/webhooks/orders")

	require.NoError(t, runner.run(t, runner.activities.NotifyVenueCancelled, orderworkflow.CancelNotice{
		OrderID: created.ID, Reason: "заведение не подтвердило заказ", CancelledBy: "system",
	}))

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, "/pos/webhooks/orders/"+created.ID.String()+"/cancelled", gotPath)
	assert.Contains(t, string(body), `"cancelled_by":"system"`)
}

// Заказа нет у заведения — 404 на отмене тоже успех: снимать нечего.
func TestActivityNotifyVenueCancelledTreatsNotFoundAsSuccess(t *testing.T) {
	runner := newActivities(t)
	created := createdOrder(t)

	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer venue.Close()

	registerWebhook(t, venue.URL+"/pos/webhooks/orders")

	require.NoError(t, runner.run(t, runner.activities.NotifyVenueCancelled, orderworkflow.CancelNotice{
		OrderID: created.ID, Reason: "отменён пользователем", CancelledBy: "user",
	}))
}

// Курьер в MVP замокан, но шаг в процессе явный: его потом заменит вызов
// настоящего сервиса.
func TestActivityAssignCourier(t *testing.T) {
	runner := newActivities(t)

	value, err := runner.env.ExecuteActivity(runner.activities.AssignCourier, uuid.New())
	require.NoError(t, err)

	var courier string
	require.NoError(t, value.Get(&courier))
	assert.NotEmpty(t, courier)
}

func paymentStatus(t *testing.T, orderID uuid.UUID) string {
	t.Helper()

	var status string
	require.NoError(t, pool.QueryRow(testContext(t),
		`SELECT status FROM payments WHERE order_id = $1`, orderID).Scan(&status))

	return status
}

func registerWebhook(t *testing.T, url string) {
	t.Helper()

	_, err := pool.Exec(testContext(t), `
		INSERT INTO venue_webhooks (venue_id, url, secret, is_active)
		VALUES ($1, $2, $3, true)
		ON CONFLICT (venue_id) DO UPDATE SET url = EXCLUDED.url, secret = EXCLUDED.secret`,
		demoVenue, url, "секрет-подписи")
	require.NoError(t, err)

	t.Cleanup(func() {
		_, deleteErr := pool.Exec(testContext(t),
			`DELETE FROM venue_webhooks WHERE venue_id = $1`, demoVenue)
		require.NoError(t, deleteErr)
	})
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()

	body := make([]byte, r.ContentLength)
	if r.ContentLength <= 0 {
		return nil
	}

	_, err := r.Body.Read(body)
	if err != nil && err.Error() != "EOF" {
		require.NoError(t, err)
	}

	return body
}

// Суточная сверка ходит к заведению подписанным запросом и сравнивает его
// меню с каталогом. Расхождения она считает, а не чинит: тихая правка данных
// по расписанию прячет проблему интеграции.
func TestActivityReconcileMenusCountsDrift(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	activities := orderworkflow.NewReconcileActivities(merchant.NewRepository(pool))
	env.RegisterActivity(activities)

	var (
		mu      sync.Mutex
		gotPath string
		headers http.Header
	)

	// Заведение отдаёт три позиции: одна совпадает, у второй разошлась цена,
	// третьей нет в каталоге. Плюс шесть позиций каталога, которых нет здесь.
	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		headers = r.Header.Clone()
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		// Ошибку записи не проверяем через require: в хендлере это увело бы
		// падение в чужую горутину, где testing её не поймает.
		_, _ = w.Write([]byte(`{"items":[
			{"external_id":"tn-001","price_minor":12000,"is_available":true},
			{"external_id":"tn-002","price_minor":99000,"is_available":true},
			{"external_id":"tn-999","price_minor":10000,"is_available":true}
		]}`))
	}))
	defer venue.Close()

	_, err := pool.Exec(testContext(t), `
		INSERT INTO venue_webhooks (venue_id, url, secret, is_active)
		VALUES ($1, $2, $3, true)
		ON CONFLICT (venue_id) DO UPDATE SET url = EXCLUDED.url, secret = EXCLUDED.secret`,
		otherVenue, venue.URL+"/pos/webhooks/orders", "секрет-сверки")
	require.NoError(t, err)

	t.Cleanup(func() {
		_, deleteErr := pool.Exec(testContext(t),
			`DELETE FROM venue_webhooks WHERE venue_id = $1`, otherVenue)
		require.NoError(t, deleteErr)
	})

	value, err := env.ExecuteActivity(activities.ReconcileMenus)
	require.NoError(t, err)

	var report orderworkflow.ReconcileReport
	require.NoError(t, value.Get(&report))

	assert.Equal(t, 1, report.VenuesChecked)
	// Разошлись: цена tn-002, лишняя у заведения tn-999 и шесть позиций
	// каталога, которых заведение не отдало.
	assert.Equal(t, 8, report.DriftCount)

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, "/pos/webhooks/menu", gotPath, "меню спрашивается по своему пути")
	assert.Regexp(t, `^sha256=[0-9a-f]{64}$`, headers.Get("X-Kitchen-Signature"),
		"для заведения это такой же входящий вызов платформы")
}

// Недоступное заведение не валит сверку остальных: это диагностика,
// а не операция над данными.
func TestActivityReconcileMenusSurvivesUnavailableVenue(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	activities := orderworkflow.NewReconcileActivities(merchant.NewRepository(pool))
	env.RegisterActivity(activities)

	venue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer venue.Close()

	_, err := pool.Exec(testContext(t), `
		INSERT INTO venue_webhooks (venue_id, url, secret, is_active)
		VALUES ($1, $2, $3, true)
		ON CONFLICT (venue_id) DO UPDATE SET url = EXCLUDED.url`,
		otherVenue, venue.URL+"/pos/webhooks/orders", "секрет-сверки")
	require.NoError(t, err)

	t.Cleanup(func() {
		_, deleteErr := pool.Exec(testContext(t),
			`DELETE FROM venue_webhooks WHERE venue_id = $1`, otherVenue)
		require.NoError(t, deleteErr)
	})

	value, err := env.ExecuteActivity(activities.ReconcileMenus)
	require.NoError(t, err, "недоступное заведение не должно валить сверку")

	var report orderworkflow.ReconcileReport
	require.NoError(t, value.Get(&report))

	assert.Equal(t, 1, report.VenuesChecked)
	assert.Zero(t, report.DriftCount, "с недоступного заведения расхождений не снимают")
}

// Возврат в продажу по таймеру стоп-листа — обычная активность над базой,
// и она идемпотентна: повтор на уже доступной позиции ничего не меняет.
func TestActivityReturnToSaleIsIdempotent(t *testing.T) {
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	activities := orderworkflow.NewStopListActivities(merchant.NewRepository(pool))
	env.RegisterActivity(activities)

	_, err := pool.Exec(testContext(t),
		`UPDATE menu_items SET is_available = false WHERE id = $1`, demoItem)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, restoreErr := pool.Exec(testContext(t),
			`UPDATE menu_items SET is_available = true WHERE id = $1`, demoItem)
		require.NoError(t, restoreErr)
	})

	input := orderworkflow.StopListInput{
		VenueID: demoVenue, Kind: merchant.StopTargetItem, TargetID: demoItem,
	}

	for range 2 {
		_, err = env.ExecuteActivity(activities.ReturnToSale, input)
		require.NoError(t, err)
	}

	var available bool
	require.NoError(t, pool.QueryRow(testContext(t),
		`SELECT is_available FROM menu_items WHERE id = $1`, demoItem).Scan(&available))
	assert.True(t, available)
}
