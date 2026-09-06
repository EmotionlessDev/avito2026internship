package public

import (
	"context"
	"net/http"

	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
)

// Ручки корзины и заказов появляются на этапах 5–7. До тех пор они честно
// отвечают 501, а не молчат и не падают: контракт опубликован целиком,
// и клиент должен видеть разницу между «ручки нет» и «ручка ещё не готова».
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

const (
	stageCart   = "5 (корзина и каскад валидации)"
	stageOrders = "6–7 (заказы, outbox и Temporal)"
)

func (n notImplemented) VisitGetCartResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitClearCartResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitAddCartItemResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitUpdateCartItemResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitDeleteCartItemResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitValidateCartResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitListOrdersResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitCreateOrderResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitGetOrderResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitCancelOrderResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitStreamOrderEventsResponse(w http.ResponseWriter) error {
	return n.write(w)
}

// GetCart — этап 5.
func (h *Handler) GetCart(
	_ context.Context, _ public.GetCartRequestObject,
) (public.GetCartResponseObject, error) {
	return notImplemented{stage: stageCart}, nil
}

// ClearCart — этап 5.
func (h *Handler) ClearCart(
	_ context.Context, _ public.ClearCartRequestObject,
) (public.ClearCartResponseObject, error) {
	return notImplemented{stage: stageCart}, nil
}

// AddCartItem — этап 5.
func (h *Handler) AddCartItem(
	_ context.Context, _ public.AddCartItemRequestObject,
) (public.AddCartItemResponseObject, error) {
	return notImplemented{stage: stageCart}, nil
}

// UpdateCartItem — этап 5.
func (h *Handler) UpdateCartItem(
	_ context.Context, _ public.UpdateCartItemRequestObject,
) (public.UpdateCartItemResponseObject, error) {
	return notImplemented{stage: stageCart}, nil
}

// DeleteCartItem — этап 5.
func (h *Handler) DeleteCartItem(
	_ context.Context, _ public.DeleteCartItemRequestObject,
) (public.DeleteCartItemResponseObject, error) {
	return notImplemented{stage: stageCart}, nil
}

// ValidateCart — этап 5.
func (h *Handler) ValidateCart(
	_ context.Context, _ public.ValidateCartRequestObject,
) (public.ValidateCartResponseObject, error) {
	return notImplemented{stage: stageCart}, nil
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
