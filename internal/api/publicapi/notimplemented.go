package publicapi

import (
	"context"
	"net/http"

	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
)

// Ручки заказов появляются на этапах 6–7. До тех пор они честно отвечают 501,
// а не молчат и не падают: контракт опубликован целиком, и клиент должен
// видеть разницу между «ручки нет» и «ручка ещё не готова».
//
// Заглушка одна на все ручки: strict-сервер требует, чтобы интерфейс был
// реализован полностью, и пустые методы-заглушки в каждом файле только
// размазали бы этот факт по коду.
type notImplemented struct {
	stage string
}

func (n notImplemented) write(w http.ResponseWriter) error {
	return httpx.EncodeProblem(w, httpx.NewProblem(
		http.StatusNotImplemented,
		"NOT_IMPLEMENTED",
		"Ручка ещё не реализована",
		"Появится на этапе "+n.stage+" — см. PLAN.md",
	))
}

const stageOrders = "6–7 (заказы, outbox и Temporal)"

func (n notImplemented) VisitListOrdersResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitCreateOrderResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitGetOrderResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitCancelOrderResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitStreamOrderEventsResponse(w http.ResponseWriter) error {
	return n.write(w)
}

// ListOrders — этап 6.
func (h *Handler) ListOrders(
	_ context.Context, _ public.ListOrdersRequestObject,
) (public.ListOrdersResponseObject, error) {
	return notImplemented{stage: stageOrders}, nil
}

// CreateOrder — этап 6.
func (h *Handler) CreateOrder(
	_ context.Context, _ public.CreateOrderRequestObject,
) (public.CreateOrderResponseObject, error) {
	return notImplemented{stage: stageOrders}, nil
}

// GetOrder — этап 6.
func (h *Handler) GetOrder(
	_ context.Context, _ public.GetOrderRequestObject,
) (public.GetOrderResponseObject, error) {
	return notImplemented{stage: stageOrders}, nil
}

// CancelOrder — этап 7.
func (h *Handler) CancelOrder(
	_ context.Context, _ public.CancelOrderRequestObject,
) (public.CancelOrderResponseObject, error) {
	return notImplemented{stage: stageOrders}, nil
}

// StreamOrderEvents — этап 7.
func (h *Handler) StreamOrderEvents(
	_ context.Context, _ public.StreamOrderEventsRequestObject,
) (public.StreamOrderEventsResponseObject, error) {
	return notImplemented{stage: stageOrders}, nil
}
