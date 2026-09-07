package publicapi

import (
	"context"
	"net/http"

	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
)

// Отмена заказа и живой поток статусов — это сигналы workflow, поэтому они
// появятся вместе с Temporal. До тех пор ручки честно отвечают 501,
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

const stageOrders = "7 (Temporal: сигналы, таймеры и компенсации)"

func (n notImplemented) VisitCancelOrderResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitStreamOrderEventsResponse(w http.ResponseWriter) error {
	return n.write(w)
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
