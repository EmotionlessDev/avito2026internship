package merchantapi

import (
	"context"

	gen "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// GetVenueStats — витрина показателей заведения за сегодня (сценарий M8).
//
// Хендлер читает готовую строку проекции: считать агрегат по заказам
// в момент запроса значило бы дать отчёту тормозить оформление
// (docs/DESIGN.md §5).
func (h *Handler) GetVenueStats(
	ctx context.Context, _ gen.GetVenueStatsRequestObject,
) (gen.GetVenueStatsResponseObject, error) {
	stats, err := h.merchant.DailyStats(ctx)
	if err != nil {
		return nil, err
	}

	expired := stats.OrdersExpired

	return gen.GetVenueStats200JSONResponse{
		Date:             openapi_types.Date{Time: stats.Date},
		OrdersTotal:      stats.OrdersTotal,
		OrdersCompleted:  stats.OrdersCompleted,
		OrdersRejected:   stats.OrdersRejected,
		OrdersExpired:    &expired,
		RevenueMinor:     stats.RevenueMinor,
		AvgAcceptSeconds: stats.AvgAcceptSeconds,
	}, nil
}
