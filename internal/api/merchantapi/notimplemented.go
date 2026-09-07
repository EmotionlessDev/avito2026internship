package merchantapi

import (
	"context"
	"net/http"

	gen "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
)

// Переходы статусов заказа — сигналы workflow, поэтому они появляются
// вместе с Temporal (этап 7),
// витрина статистики — вместе с консьюмером Kafka (этап 8). До тех пор
// ручки честно отвечают 501 и называют этап: заведение должно видеть
// разницу между «ручки нет» и «ручка ещё не готова».
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
	stageOrders = "7 (Temporal: сигналы, таймеры и компенсации)"
	stageStats  = "8 (витрина статистики из событий Kafka)"
)

func (n notImplemented) VisitAcceptOrderResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitRejectOrderResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitMarkOrderReadyResponse(w http.ResponseWriter) error { return n.write(w) }

func (n notImplemented) VisitMarkOrderDeliveredResponse(w http.ResponseWriter) error {
	return n.write(w)
}

func (n notImplemented) VisitGetVenueStatsResponse(w http.ResponseWriter) error { return n.write(w) }

// AcceptOrder — этап 7.
func (h *Handler) AcceptOrder(
	_ context.Context, _ gen.AcceptOrderRequestObject,
) (gen.AcceptOrderResponseObject, error) {
	return notImplemented{stage: stageOrders}, nil
}

// RejectOrder — этап 7.
func (h *Handler) RejectOrder(
	_ context.Context, _ gen.RejectOrderRequestObject,
) (gen.RejectOrderResponseObject, error) {
	return notImplemented{stage: stageOrders}, nil
}

// MarkOrderReady — этап 7.
func (h *Handler) MarkOrderReady(
	_ context.Context, _ gen.MarkOrderReadyRequestObject,
) (gen.MarkOrderReadyResponseObject, error) {
	return notImplemented{stage: stageOrders}, nil
}

// MarkOrderDelivered — этап 7.
func (h *Handler) MarkOrderDelivered(
	_ context.Context, _ gen.MarkOrderDeliveredRequestObject,
) (gen.MarkOrderDeliveredResponseObject, error) {
	return notImplemented{stage: stageOrders}, nil
}

// GetVenueStats — этап 8.
func (h *Handler) GetVenueStats(
	_ context.Context, _ gen.GetVenueStatsRequestObject,
) (gen.GetVenueStatsResponseObject, error) {
	return notImplemented{stage: stageStats}, nil
}
