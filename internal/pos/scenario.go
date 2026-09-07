package pos

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// scenarioState — сценарные триггеры демо.
//
// Они нужны, чтобы «трудности» из docs/DESIGN.md §2–3 воспроизводились одной
// командой, а не подгадыванием момента: отказ заведения, молчание, недоступность,
// стоп-лист и подорожание. Живут в памяти — это инструмент прогона, а не данные.
type scenarioState struct {
	// rejectNext — следующий заказ будет отклонён (сценарий M5).
	rejectNext bool
	// ignoreNext — следующий заказ останется без ответа, и платформа
	// отменит его по таймеру (сценарий U10).
	ignoreNext bool
	// offlineUntil — до какого момента заведение «недоступно» (сценарий M6).
	offlineUntil time.Time
}

func (s *Service) offline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return time.Now().Before(s.scenario.offlineUntil)
}

// takeDecision отдаёт решение по очередному заказу и сбрасывает одноразовый
// триггер: «следующий» означает ровно один заказ.
func (s *Service) takeDecision() (reject, ignore bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	reject, ignore = s.scenario.rejectNext, s.scenario.ignoreNext
	s.scenario.rejectNext, s.scenario.ignoreNext = false, false

	return reject, ignore
}

func (s *Service) mountScenarios(router chi.Router) {
	router.Post("/reject-next", s.scenarioRejectNext)
	router.Post("/ignore-next", s.scenarioIgnoreNext)
	router.Post("/go-offline", s.scenarioGoOffline)
	router.Post("/stop-item/{externalID}", s.scenarioStopItem)
	router.Post("/stop-option/{externalID}", s.scenarioStopOption)
	router.Post("/raise-price/{externalID}", s.scenarioRaisePrice)
	router.Post("/reset-menu", s.scenarioResetMenu)
}

func (s *Service) scenarioRejectNext(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.scenario.rejectNext = true
	s.mu.Unlock()

	s.log.InfoContext(r.Context(), "сценарий: следующий заказ будет отклонён")
	writeJSON(r.Context(), s.log, w, http.StatusOK, map[string]string{fieldScenario: "reject-next"})
}

func (s *Service) scenarioIgnoreNext(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.scenario.ignoreNext = true
	s.mu.Unlock()

	s.log.InfoContext(r.Context(), "сценарий: следующий заказ останется без ответа")
	writeJSON(r.Context(), s.log, w, http.StatusOK, map[string]string{fieldScenario: "ignore-next"})
}

func (s *Service) scenarioGoOffline(w http.ResponseWriter, r *http.Request) {
	seconds := 30
	if raw := r.URL.Query().Get("seconds"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(r.Context(), s.log, w, http.StatusBadRequest,
				map[string]string{fieldError: "seconds должен быть положительным числом"})

			return
		}

		seconds = parsed
	}

	until := time.Now().Add(time.Duration(seconds) * time.Second)

	s.mu.Lock()
	s.scenario.offlineUntil = until
	s.mu.Unlock()

	s.log.InfoContext(r.Context(), "сценарий: заведение офлайн",
		slog.Int("seconds", seconds))

	writeJSON(r.Context(), s.log, w, http.StatusOK, map[string]any{
		fieldScenario: "go-offline", "until": until.UTC(),
	})
}

// scenarioStopItem снимает позицию с продажи — и у себя, и на платформе.
// Порядок именно такой: сначала своё меню, потом платформа, иначе при сбое
// платформа знала бы о стоп-листе, которого у заведения нет.
func (s *Service) scenarioStopItem(w http.ResponseWriter, r *http.Request) {
	externalID := chi.URLParam(r, "externalID")

	if err := s.storage.SetItemAvailability(r.Context(), externalID, false); err != nil {
		s.scenarioFailed(w, r, err)

		return
	}

	if err := s.platform.SetAvailability(r.Context(), externalID, false, false); err != nil {
		s.scenarioFailed(w, r, err)

		return
	}

	s.log.InfoContext(r.Context(), "сценарий: позиция снята с продажи",
		slog.String("external_id", externalID))

	writeJSON(r.Context(), s.log, w, http.StatusOK,
		map[string]string{fieldScenario: "stop-item", fieldExternalID: externalID})
}

// scenarioStopOption снимает модификатор: у самого POS опций в меню нет,
// поэтому здесь только вызов платформы — стоп-лист опций живёт там.
func (s *Service) scenarioStopOption(w http.ResponseWriter, r *http.Request) {
	externalID := chi.URLParam(r, "externalID")

	if err := s.platform.SetAvailability(r.Context(), externalID, false, true); err != nil {
		s.scenarioFailed(w, r, err)

		return
	}

	s.log.InfoContext(r.Context(), "сценарий: опция снята",
		slog.String("external_id", externalID))

	writeJSON(r.Context(), s.log, w, http.StatusOK,
		map[string]string{fieldScenario: "stop-option", fieldExternalID: externalID})
}

// scenarioRaisePrice поднимает цену на 15% и переотправляет меню:
// именно так это выглядит в жизни — заведение меняет цену у себя,
// а платформа узнаёт из следующего импорта (сценарий U7).
func (s *Service) scenarioRaisePrice(w http.ResponseWriter, r *http.Request) {
	externalID := chi.URLParam(r, "externalID")

	items, err := s.storage.Menu(r.Context())
	if err != nil {
		s.scenarioFailed(w, r, err)

		return
	}

	var updated int64

	for i := range items {
		if items[i].ExternalID != externalID {
			continue
		}

		updated = items[i].PriceMinor * 115 / 100
	}

	if updated == 0 {
		writeJSON(r.Context(), s.log, w, http.StatusNotFound,
			map[string]string{fieldError: "позиция не найдена в меню заведения"})

		return
	}

	if err := s.storage.SetItemPrice(r.Context(), externalID, updated); err != nil {
		s.scenarioFailed(w, r, err)

		return
	}

	if _, err := s.syncMenu(r.Context()); err != nil {
		s.scenarioFailed(w, r, err)

		return
	}

	s.log.InfoContext(r.Context(), "сценарий: цена поднята",
		slog.String("external_id", externalID), slog.Int64("price_minor", updated))

	writeJSON(r.Context(), s.log, w, http.StatusOK, map[string]any{
		fieldScenario: "raise-price", fieldExternalID: externalID, "price_minor": updated,
	})
}

// scenarioResetMenu возвращает заведение в исходное состояние и заново
// подключает его к платформе — то же, что делает онбординг (сценарий M1).
//
// Нужен демо-прогону с двух сторон: он откатывает подорожание и стоп-листы
// предыдущего прогона, а повторный вызов доказывает идемпотентность импорта
// (сценарий M2) — платформа на неизменившийся снапшот отвечает нулями.
func (s *Service) scenarioResetMenu(w http.ResponseWriter, r *http.Request) {
	seed, err := loadMenuSeed()
	if err != nil {
		s.scenarioFailed(w, r, err)

		return
	}

	if resetErr := s.storage.ResetMenu(r.Context(), seed.Items); resetErr != nil {
		s.scenarioFailed(w, r, resetErr)

		return
	}

	result, err := s.syncMenu(r.Context())
	if err != nil {
		s.scenarioFailed(w, r, err)

		return
	}

	// Вебхук регистрируется здесь же: заведение, которое отдало меню, но не
	// сказало, куда слать заказы, подключённым не считается.
	if err := s.platform.RegisterWebhook(
		r.Context(), s.cfg.WebhookURL, s.cfg.WebhookSecret,
	); err != nil {
		s.scenarioFailed(w, r, err)

		return
	}

	s.log.InfoContext(r.Context(), "сценарий: меню сброшено и переотправлено",
		slog.Int("created", result.Created),
		slog.Int("updated", result.Updated),
		slog.Int("archived", result.Archived),
		slog.Bool("idempotent", result.Idempotent))

	writeJSON(r.Context(), s.log, w, http.StatusOK, map[string]any{
		fieldScenario: "reset-menu",
		"created":     result.Created,
		"updated":     result.Updated,
		"archived":    result.Archived,
		"idempotent":  result.Idempotent,
	})
}

func (s *Service) scenarioFailed(w http.ResponseWriter, r *http.Request, err error) {
	// Неизвестный артикул — ошибка того, кто зовёт сценарий, а не платформы:
	// отвечать на неё 502 значило бы прятать опечатку за «внешний сбой».
	if errors.Is(err, ErrArtifactNotFound) {
		writeJSON(r.Context(), s.log, w, http.StatusNotFound,
			map[string]string{fieldError: err.Error()})

		return
	}

	s.log.ErrorContext(r.Context(), "сценарий не отработал", slog.String("error", err.Error()))
	writeJSON(r.Context(), s.log, w, http.StatusBadGateway, map[string]string{fieldError: err.Error()})
}
