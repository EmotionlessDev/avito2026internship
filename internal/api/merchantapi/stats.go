package merchantapi

import (
	"context"
	"time"

	gen "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// GetVenueStats — витрина показателей заведения за день (сценарий M8).
//
// Хендлер читает готовую строку проекции: считать агрегат по заказам
// в момент запроса значило бы дать отчёту тормозить оформление
// (docs/DESIGN.md §5).
func (h *Handler) GetVenueStats(
	ctx context.Context, request gen.GetVenueStatsRequestObject,
) (gen.GetVenueStatsResponseObject, error) {
	stats, err := h.merchant.DailyStats(ctx, askedDay(request.Params.Date))
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

// askedDay переводит день из запроса в домен. Пустой параметр — «сегодня»,
// и какой это день, решает часовой пояс заведения, а не сервера.
func askedDay(date *openapi_types.Date) *time.Time {
	if date == nil {
		return nil
	}

	return &date.Time
}
