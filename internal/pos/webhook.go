package pos

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	pos "github.com/emotionlessdev/avito-kitchen/internal/gen/pos"
)

// Приём вебхуков платформы. Контракт — api/pos-webhooks.openapi.yaml,
// который реализует заведение: платформа здесь клиент, а не сервер.

// DeliverOrder принимает новый заказ.
//
// Порядок проверок: подпись, окно времени, дедупликация. Только потом заказ
// попадает на кухню. Ответ 202 означает «принял в обработку», а не
// «подтвердил»: подтверждение уходит отдельным вызовом Merchant API.
func (s *Service) DeliverOrder(
	ctx context.Context, request pos.DeliverOrderRequestObject,
) (pos.DeliverOrderResponseObject, error) {
	if s.offline() {
		// Сценарий M6: заведение недоступно. Платформа повторит вызов,
		// а если не достучится — заказ подхватит поллинг.
		s.log.InfoContext(ctx, "заведение офлайн, вебхук отклонён")

		return pos.DeliverOrder503Response{}, nil
	}

	if request.Body == nil {
		return pos.DeliverOrder400ApplicationProblemPlusJSONResponse{}, nil
	}

	fresh, err := s.storage.MarkEventProcessed(ctx, request.Params.XKitchenEventId.String())
	if err != nil {
		return nil, err
	}

	if !fresh {
		// Повторная доставка — норма при at-least-once. Для платформы это
		// успех: 409 она считает подтверждением.
		return pos.DeliverOrder409Response{}, nil
	}

	payload, err := json.Marshal(request.Body.Order)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	saved, err := s.storage.SaveOrder(ctx, Order{
		ID:         request.Body.Order.Id.String(),
		Number:     request.Body.Order.Number,
		Status:     statusNew,
		Payload:    string(payload),
		ReceivedAt: now,
		ActAfter:   now.Add(s.cfg.AcceptDelay),
	})
	if err != nil {
		return nil, err
	}

	if saved {
		s.log.InfoContext(ctx, "заказ принят на кухню",
			slog.String("order", request.Body.Order.Number),
			slog.Int("items", len(request.Body.Order.Items)))
	}

	return pos.DeliverOrder202Response{}, nil
}

// DeliverOrderCancelled снимает заказ с производства.
func (s *Service) DeliverOrderCancelled(
	ctx context.Context, request pos.DeliverOrderCancelledRequestObject,
) (pos.DeliverOrderCancelledResponseObject, error) {
	fresh, err := s.storage.MarkEventProcessed(ctx, request.Params.XKitchenEventId.String())
	if err != nil {
		return nil, err
	}

	if !fresh {
		return pos.DeliverOrderCancelled409Response{}, nil
	}

	if err := s.storage.SetStatus(ctx, request.OrderId.String(), statusCancelled, time.Now().UTC()); err != nil {
		return nil, err
	}

	s.log.InfoContext(ctx, "заказ отменён платформой",
		slog.String("order_id", request.OrderId.String()))

	return pos.DeliverOrderCancelled202Response{}, nil
}

// GetPosMenu отдаёт меню заведения для суточной сверки.
func (s *Service) GetPosMenu(
	ctx context.Context, _ pos.GetPosMenuRequestObject,
) (pos.GetPosMenuResponseObject, error) {
	items, err := s.storage.Menu(ctx)
	if err != nil {
		return nil, err
	}

	response := pos.PosMenu{Items: make([]pos.PosMenuItem, 0, len(items))}
	generated := time.Now().UTC()
	response.GeneratedAt = &generated

	for i := range items {
		response.Items = append(response.Items, pos.PosMenuItem{
			ExternalId:  items[i].ExternalID,
			Name:        items[i].Name,
			PriceMinor:  items[i].PriceMinor,
			IsAvailable: items[i].IsAvailable,
		})
	}

	return pos.GetPosMenu200JSONResponse(response), nil
}

// verifySignature проверяет подпись и окно времени вебхука.
//
// Подпись без проверки времени бесполезна против повтора: перехваченный
// запрос остался бы валидным навсегда (docs/DESIGN.md §10).
func (s *Service) verifySignature(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signature := r.Header.Get("X-Kitchen-Signature")
		timestamp := r.Header.Get("X-Kitchen-Timestamp")

		if signature == "" || timestamp == "" {
			s.rejectWebhook(w, r, "нет подписи или времени")

			return
		}

		sentAt, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			s.rejectWebhook(w, r, "некорректный X-Kitchen-Timestamp")

			return
		}

		if skew := time.Since(sentAt); skew > s.cfg.WebhookMaxSkew || skew < -s.cfg.WebhookMaxSkew {
			s.rejectWebhook(w, r, "запрос вне окна времени")

			return
		}

		body, err := drainBody(r)
		if err != nil {
			s.rejectWebhook(w, r, "не удалось прочитать тело")

			return
		}

		if !s.validSignature(signature, timestamp, body) {
			s.rejectWebhook(w, r, "подпись не сошлась")

			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Service) validSignature(signature, timestamp string, body []byte) bool {
	mac := hmac.New(sha256.New, []byte(s.cfg.WebhookSecret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)

	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Сравнение постоянного времени: обычное сравнение строк подсказало бы
	// атакующему, сколько символов подписи он угадал.
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

func (s *Service) rejectWebhook(w http.ResponseWriter, r *http.Request, reason string) {
	s.log.WarnContext(r.Context(), "вебхук отклонён", slog.String("reason", reason))

	writeJSON(r.Context(), s.log, w, http.StatusUnauthorized, map[string]string{
		"type":   "https://kitchen.avito.ru/errors/invalid-signature",
		"title":  "Подпись не принята",
		"code":   "INVALID_SIGNATURE",
		"detail": reason,
	})
}
