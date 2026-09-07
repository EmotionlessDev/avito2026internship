package pos

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	pos "github.com/emotionlessdev/avito-kitchen/internal/gen/pos"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// webhookBasePath — префикс, который заведение сообщает платформе.
// Он же указан как сервер в api/pos-webhooks.openapi.yaml.
const webhookBasePath = "/pos/webhooks"

// router собирает маршруты заведения.
//
// Три группы с разными хозяевами: вебхуки описаны контрактом платформы,
// операторские ручки — это «кнопки повара», сценарные триггеры нужны демо.
func (s *Service) router() http.Handler {
	router := chi.NewRouter()

	router.Get("/healthz", s.health)
	router.Get("/readyz", s.health)

	// Вебхуки: маршруты и разбор тела берутся из сгенерированного кода.
	//
	// Проверка подписи навешена на подроутер, а не передана генератору
	// списком middleware: сгенерированная обёртка сначала разбирает
	// обязательные заголовки и только потом зовёт middleware, поэтому запрос
	// без подписи получал бы 400 «нет заголовка» вместо честного 401.
	webhooks := chi.NewRouter()
	webhooks.Use(s.verifySignature)

	strict := pos.NewStrictHandler(s, nil)
	pos.HandlerWithOptions(strict, pos.ChiServerOptions{
		BaseRouter: webhooks,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSON(r.Context(), s.log, w, http.StatusBadRequest,
				map[string]string{fieldError: err.Error()})
		},
	})

	router.Mount(webhookBasePath, webhooks)

	router.Route("/pos/orders", func(r chi.Router) {
		r.Get("/", s.listOrders)
		r.Post("/{orderID}/accept", s.operatorAccept)
		r.Post("/{orderID}/reject", s.operatorReject)
		r.Post("/{orderID}/ready", s.operatorReady)
	})

	router.Route("/pos/scenario", s.mountScenarios)

	return router
}

func (s *Service) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(r.Context(), s.log, w, http.StatusOK, map[string]string{"status": "ok"})
}

// listOrders — экран повара: что сейчас на кухне.
func (s *Service) listOrders(w http.ResponseWriter, r *http.Request) {
	const limit = 50

	orders, err := s.storage.ListOrders(r.Context(), limit)
	if err != nil {
		s.operatorFailed(w, r, err)

		return
	}

	type view struct {
		ID         string `json:"id"`
		Number     string `json:"number"`
		Status     string `json:"status"`
		ReceivedAt string `json:"received_at"`
	}

	items := make([]view, 0, len(orders))
	for i := range orders {
		items = append(items, view{
			ID:         orders[i].ID,
			Number:     orders[i].Number,
			Status:     orders[i].Status,
			ReceivedAt: orders[i].ReceivedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	writeJSON(r.Context(), s.log, w, http.StatusOK, map[string]any{"items": items})
}

// Операторские ручки — то, что в жизни нажимает повар. Они работают и при
// выключенном автопилоте: демо показывает автоматику, а руками можно всё то же.

func (s *Service) operatorAccept(w http.ResponseWriter, r *http.Request) {
	s.operate(w, r, statusAccepted, func(ctx context.Context, orderID uuid.UUID) error {
		return s.platform.Accept(ctx, orderID, etaMinutes(s.cfg))
	})
}

func (s *Service) operatorReject(w http.ResponseWriter, r *http.Request) {
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "заведение не может выполнить заказ"
	}

	s.operate(w, r, statusRejected, func(ctx context.Context, orderID uuid.UUID) error {
		return s.platform.Reject(ctx, orderID, reason)
	})
}

func (s *Service) operatorReady(w http.ResponseWriter, r *http.Request) {
	s.operate(w, r, statusReady, func(ctx context.Context, orderID uuid.UUID) error {
		return s.platform.Ready(ctx, orderID)
	})
}

// operate выполняет действие повара: сообщает платформе и переводит заказ
// в новый статус у себя.
func (s *Service) operate(
	w http.ResponseWriter, r *http.Request, status string,
	action func(context.Context, uuid.UUID) error,
) {
	raw := chi.URLParam(r, "orderID")

	orderID, err := uuid.Parse(raw)
	if err != nil {
		writeJSON(r.Context(), s.log, w, http.StatusBadRequest,
			map[string]string{fieldError: "идентификатор заказа должен быть UUID"})

		return
	}

	order, err := s.storage.OrderByID(r.Context(), raw)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			writeJSON(r.Context(), s.log, w, http.StatusNotFound,
				map[string]string{fieldError: "заказ не найден"})

			return
		}

		s.operatorFailed(w, r, err)

		return
	}

	if err := action(r.Context(), orderID); err != nil {
		s.operatorFailed(w, r, err)

		return
	}

	next := nextActionTime(s.cfg, status)
	if err := s.storage.SetStatus(r.Context(), order.ID, status, next); err != nil {
		s.operatorFailed(w, r, err)

		return
	}

	s.log.InfoContext(r.Context(), "повар изменил статус заказа",
		slog.String("order", order.Number), slog.String("status", status))

	writeJSON(r.Context(), s.log, w, http.StatusOK,
		map[string]string{"id": order.ID, "number": order.Number, "status": status})
}

// etaMinutes переводит время готовки в минуты контракта: ETA там целое,
// а демо-готовка занимает секунды, поэтому меньше минуты не бывает.
func etaMinutes(cfg Config) int {
	minutes := int(cfg.PrepTime.Minutes())
	if minutes < 1 {
		return 1
	}

	return minutes
}

// nextActionTime говорит, когда автопилот вернётся к заказу: после ручного
// подтверждения он доведёт его до конца сам.
func nextActionTime(cfg Config, status string) time.Time {
	now := time.Now().UTC()

	switch status {
	case statusAccepted:
		return now.Add(cfg.PrepTime)
	case statusReady:
		return now.Add(cfg.DeliveryTime)
	default:
		return now
	}
}

func (s *Service) operatorFailed(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "операторская ручка не отработала",
		slog.String("error", err.Error()))

	writeJSON(r.Context(), s.log, w, http.StatusBadGateway, map[string]string{fieldError: err.Error()})
}
