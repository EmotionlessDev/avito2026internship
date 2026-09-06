package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/config"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRouter(t *testing.T, health *httpx.Health) http.Handler {
	t.Helper()

	// Логи тестов не нужны в выводе.
	log := slog.New(slog.DiscardHandler)

	return httpx.NewRouter(log, health)
}

func TestLivenessIgnoresDependencies(t *testing.T) {
	health := httpx.NewHealth()
	health.Register("postgres", func(context.Context) error {
		return errors.New("connection refused")
	})

	rec := httptest.NewRecorder()
	testRouter(t, health).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody))

	// Ключевое различие проб: упавшая зависимость не повод перезапускать
	// живой процесс — рестарт не поднимет чужую БД.
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestReadinessReportsFailingCheck(t *testing.T) {
	health := httpx.NewHealth()
	health.Register("postgres", func(context.Context) error { return nil })
	health.Register("kafka", func(context.Context) error {
		return errors.New("no brokers available")
	})

	rec := httptest.NewRecorder()
	testRouter(t, health).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, "fail", body.Status)
	assert.Equal(t, "ok", body.Checks["postgres"].Status)
	assert.Equal(t, "fail", body.Checks["kafka"].Status)
	assert.Contains(t, body.Checks["kafka"].Error, "no brokers", "в ответе видно, что именно упало")
}

func TestReadinessWithoutChecksIsReady(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter(t, httpx.NewHealth()).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequestIDGeneratedAndReturned(t *testing.T) {
	var seen string

	r := testRouterWith(t, func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestIDFrom(r.Context())
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/probe", http.NoBody))

	assert.NotEmpty(t, seen, "хендлер видит request-id в контексте")
	assert.Equal(t, seen, rec.Header().Get(httpx.HeaderRequestID), "клиент получает тот же id")
}

func TestRequestIDFromClientIsPreserved(t *testing.T) {
	const incoming = "018f0000-0000-7000-8000-000000000abc"

	var seen string

	r := testRouterWith(t, func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestIDFrom(r.Context())
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/probe", http.NoBody)
	req.Header.Set(httpx.HeaderRequestID, incoming)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	// Сохраняем чужой id, чтобы трассировка склеилась со вызывающей системой.
	assert.Equal(t, incoming, seen)
	assert.Equal(t, incoming, rec.Header().Get(httpx.HeaderRequestID))
}

func TestPanicBecomesInternalError(t *testing.T) {
	r := testRouterWith(t, func(http.ResponseWriter, *http.Request) {
		panic("что-то пошло не так")
	})

	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/probe", http.NoBody))
	}, "паника в хендлере не должна ронять процесс")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func testRouterWith(t *testing.T, h http.HandlerFunc) http.Handler {
	t.Helper()

	log := slog.New(slog.DiscardHandler)

	r := httpx.NewRouter(log, httpx.NewHealth())
	r.Get("/probe", h)

	return r
}

func TestServerShutsDownOnContextCancel(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	srv := httpx.NewServer(
		config.HTTP{Addr: "127.0.0.1:0"},
		config.App{ShutdownTimeout: 5 * time.Second},
		http.NotFoundHandler(),
		log,
	)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- srv.Run(ctx) }()

	cancel()

	// Отмена контекста обязана завершить Run без ошибки: иначе `docker compose
	// down` будет ждать таймаута и убивать контейнер по SIGKILL.
	require.NoError(t, <-done)
}
