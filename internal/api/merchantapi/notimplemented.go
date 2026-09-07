package merchantapi

import (
	"context"
	"net/http"

	gen "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
)

// Витрина статистики появится вместе с консьюмером Kafka (этап 8). До тех пор
// ручка честно отвечает 501 и называет этап: заведение должно видеть разницу
// между «ручки нет» и «ручка ещё не готова».
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

const stageStats = "8 (витрина статистики из событий Kafka)"

func (n notImplemented) VisitGetVenueStatsResponse(w http.ResponseWriter) error { return n.write(w) }

// GetVenueStats — этап 8.
func (h *Handler) GetVenueStats(
	_ context.Context, _ gen.GetVenueStatsRequestObject,
) (gen.GetVenueStatsResponseObject, error) {
	return notImplemented{stage: stageStats}, nil
}
