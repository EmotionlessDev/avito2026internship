package merchantapi

import (
	"context"
	"errors"
	"net/http"

	gen "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/order"
	"github.com/emotionlessdev/avito-kitchen/internal/paging"
)

// Чтение заказов заведением. Переходы статусов (accept, reject, ready,
// delivered) — сигналы workflow: хендлер проверяет право на переход
// и шлёт сигнал, а статус пишет активность внутри процесса.

// ListVenueOrders — лента заказов заведения.
//
// Это же поллинг-фолбэк: если вебхук не дошёл, POS забирает новые заказы
// отсюда (сценарий M6).
func (h *Handler) ListVenueOrders(
	ctx context.Context, request gen.ListVenueOrdersRequestObject,
) (gen.ListVenueOrdersResponseObject, error) {
	venueID, err := merchant.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}

	// Курсор непрозрачен для клиента: испорченный курсор — ошибка запроса,
	// а не сбой сервиса, и разбирать её текст клиенту незачем.
	offset, ok := decodeCursor(request.Params.Cursor)
	if !ok {
		return gen.ListVenueOrders400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badRequest(ctx,
				"Курсор нужно брать из поля next_cursor предыдущего ответа"),
		}, nil
	}

	limit := pageLimit(request.Params.Limit)

	orders, err := h.orders.ListForVenue(ctx, venueID, order.Filter{
		Statuses:     venueOrderStatuses(request.Params.Status),
		UpdatedSince: request.Params.Since,
		Limit:        limit + 1,
		Offset:       offset,
	})
	if err != nil {
		return nil, err
	}

	page, next := trimOrders(orders, offset, limit)

	items := make([]gen.MerchantOrder, 0, len(page))
	for i := range page {
		items = append(items, merchantOrder(page[i]))
	}

	return gen.ListVenueOrders200JSONResponse{Items: items, NextCursor: nextCursor(next)}, nil
}

// GetVenueOrder — карточка заказа глазами заведения.
func (h *Handler) GetVenueOrder(
	ctx context.Context, request gen.GetVenueOrderRequestObject,
) (gen.GetVenueOrderResponseObject, error) {
	venueID, err := merchant.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}

	found, err := h.orders.GetForVenue(ctx, venueID, request.OrderId)
	if err != nil {
		if errors.Is(err, order.ErrNotFound) {
			// Чужой заказ неотличим от несуществующего: существование
			// чужого заказа — тоже информация (docs/DESIGN.md §8).
			return gen.GetVenueOrder404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(
					problem(ctx, http.StatusNotFound, "ORDER_NOT_FOUND", "Заказ не найден",
						"Заказа не существует или он принадлежит другому заведению"),
				),
			}, nil
		}

		return nil, err
	}

	return gen.GetVenueOrder200JSONResponse(merchantOrder(found)), nil
}

// decodeCursor разбирает курсор страницы. Отдаёт признак вместо ошибки:
// причина у неё одна — курсор испорчен, и текст ошибки в ответ не идёт.
func decodeCursor(cursor *string) (offset int, ok bool) {
	decoded, err := paging.DecodeOffset(value(cursor))
	if err != nil {
		return 0, false
	}

	return decoded, true
}

func venueOrderStatuses(statuses *[]gen.OrderStatus) []order.Status {
	if statuses == nil {
		return nil
	}

	converted := make([]order.Status, 0, len(*statuses))
	for _, status := range *statuses {
		converted = append(converted, order.Status(status))
	}

	return converted
}

func trimOrders(orders []order.Order, offset, limit int) (page []order.Order, nextCursor string) {
	if len(orders) <= limit {
		return orders, ""
	}

	return orders[:limit], paging.EncodeOffset(offset + limit)
}

func pageLimit(limit *int) int {
	const defaultLimit = 20

	if limit == nil || *limit <= 0 {
		return defaultLimit
	}

	return *limit
}

func nextCursor(cursor string) *string {
	if cursor == "" {
		return nil
	}

	return &cursor
}

func value[T any](ptr *T) T {
	var zero T

	if ptr == nil {
		return zero
	}

	return *ptr
}
