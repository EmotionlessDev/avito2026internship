package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/logger"
)

// checkTimeout ограничивает одну проверку готовности: readyz дёргает
// оркестратор часто, и он не должен висеть на залипшем соединении с БД.
const checkTimeout = 2 * time.Second

// CheckFunc — проверка внешней зависимости. nil-ошибка означает «готов».
type CheckFunc func(ctx context.Context) error

// Health разводит два разных вопроса, которые часто путают:
//
//   - /healthz (liveness) — «процесс жив?». Отвечает всегда, пока жив.
//     Если завязать его на БД, оркестратор будет перезапускать здоровый
//     сервис из-за недоступной зависимости — и рестарт ничего не починит.
//   - /readyz (readiness) — «можно слать трафик?». Прогоняет проверки
//     зависимостей; при провале сервис выводится из балансировки, но живёт.
//
// На этапе 0 проверок нет; Postgres, Kafka и Temporal регистрируются здесь
// на этапах 1, 6 и 7 — сигнатура Register для этого и заведена.
type Health struct {
	mu     sync.RWMutex
	checks map[string]CheckFunc
}

// NewHealth создаёт реестр проверок готовности.
func NewHealth() *Health {
	return &Health{checks: make(map[string]CheckFunc)}
}

// Register добавляет проверку готовности под именем name.
func (h *Health) Register(name string, check CheckFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.checks[name] = check
}

// Live — liveness-пробa: 200, пока процесс отвечает.
func (h *Health) Live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, statusResponse{Status: statusOK})
}

// Ready — readiness-проба: 200, если все зависимости отвечают, иначе 503
// с перечнем того, что именно не отвечает.
func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	checks := maps.Clone(h.checks)
	h.mu.RUnlock()

	resp := statusResponse{Status: statusOK, Checks: make(map[string]checkResult, len(checks))}
	code := http.StatusOK

	for name, check := range checks {
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		err := check(ctx)
		cancel()

		if err != nil {
			resp.Status = statusFail
			resp.Checks[name] = checkResult{Status: statusFail, Error: err.Error()}
			code = http.StatusServiceUnavailable

			continue
		}

		resp.Checks[name] = checkResult{Status: statusOK}
	}

	writeJSON(w, r, code, resp)
}

const (
	statusOK   = "ok"
	statusFail = "fail"
)

type statusResponse struct {
	Status string                 `json:"status"`
	Checks map[string]checkResult `json:"checks,omitempty"`
}

type checkResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// writeJSON отдаёт тело ответа. Ошибку записи логируем, но не пытаемся
// превратить в HTTP-статус: заголовки уже ушли клиенту.
func writeJSON(w http.ResponseWriter, r *http.Request, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		logger.From(r.Context()).WarnContext(r.Context(), "не удалось записать тело ответа",
			slog.String("error", err.Error()))
	}
}
