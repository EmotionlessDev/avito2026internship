package publicapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/order"
	"github.com/emotionlessdev/avito-kitchen/internal/paging"
)

// Ручки заказов. Отмена и SSE-поток появятся вместе с Temporal (этап 7):
// это сигналы workflow, а не запись в таблицу.

// CreateOrder — оформить заказ.
func (h *Handler) CreateOrder(
	ctx context.Context, request public.CreateOrderRequestObject,
) (public.CreateOrderResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.CreateOrder401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	if request.Body == nil {
		return public.CreateOrder400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: public.BadRequestApplicationProblemPlusJSONResponse(
				problem(ctx, http.StatusBadRequest, "BAD_REQUEST",
					"Некорректный запрос", "Тело запроса обязательно"),
			),
		}, nil
	}

	created, isNew, err := h.orders.Create(ctx, userID, order.CreateRequest{
		IdempotencyKey: request.Params.IdempotencyKey,
		Delivery: order.Delivery{
			Address: request.Body.Delivery.Address,
			Point: order.Point{
				Lat: request.Body.Delivery.Lat,
				Lon: request.Body.Delivery.Lon,
			},
		},
		ContactPhone:       request.Body.ContactPhone,
		Comment:            deref(request.Body.Comment),
		AcceptPriceChanges: deref(request.Body.AcceptPriceChanges),
	})
	if err != nil {
		return createOrderError(ctx, err)
	}

	// 201 — заказ создан сейчас, 200 — повтор с тем же ключом идемпотентности.
	// Разница нужна клиенту, чтобы отличить свой ретрай от нового заказа.
	if !isNew {
		return public.CreateOrder200JSONResponse(orderResponse(created)), nil
	}

	return public.CreateOrder201JSONResponse(orderResponse(created)), nil
}

// ListOrders — история заказов пользователя.
func (h *Handler) ListOrders(
	ctx context.Context, request public.ListOrdersRequestObject,
) (public.ListOrdersResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.ListOrders401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	// Испорченный курсор — ошибка запроса, а не отказ авторизации.
	offset, ok := decodeCursor(request.Params.Cursor)
	if !ok {
		return public.ListOrders400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badCursor(ctx),
		}, nil
	}

	limit := limit(request.Params.Limit)

	orders, err := h.orders.List(ctx, userID, order.Filter{
		Statuses: orderStatuses(request.Params.Status),
		// Запрашиваем на одну больше страницы: так становится видно,
		// есть ли следующая, без отдельного COUNT.
		Limit:  limit + 1,
		Offset: offset,
	})
	if err != nil {
		return nil, err
	}

	page, next := trimPage(orders, offset, limit)

	items := make([]public.Order, 0, len(page))
	for i := range page {
		items = append(items, orderResponse(page[i]))
	}

	return public.ListOrders200JSONResponse{Items: items, NextCursor: nextCursor(next)}, nil
}

// GetOrder — карточка заказа с таймлайном.
func (h *Handler) GetOrder(
	ctx context.Context, request public.GetOrderRequestObject,
) (public.GetOrderResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.GetOrder401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	found, err := h.orders.Get(ctx, userID, request.OrderId)
	if err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return public.GetOrder404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: orderNotFound(ctx),
			}, nil
		}

		return nil, err
	}

	return public.GetOrder200JSONResponse(orderResponse(found)), nil
}

func createOrderError(ctx context.Context, err error) (public.CreateOrderResponseObject, error) {
	var validation order.ValidationError
	if errors.As(err, &validation) {
		problem := problem(ctx, http.StatusUnprocessableEntity, "CART_NOT_VALID",
			"Корзину нельзя оформить",
			"Проверьте issues: часть проблем снимается флагом accept_price_changes")

		issues := make([]public.CartIssue, 0, len(validation.Issues))
		for i := range validation.Issues {
			issues = append(issues, cartIssue(validation.Issues[i]))
		}

		return public.CreateOrder422ApplicationProblemPlusJSONResponse{
			Type:     problem.Type,
			Title:    problem.Title,
			Status:   problem.Status,
			Code:     problem.Code,
			Detail:   problem.Detail,
			Instance: problem.Instance,
			Issues:   issues,
		}, nil
	}

	return nil, err
}

func orderNotFound(ctx context.Context) public.NotFoundApplicationProblemPlusJSONResponse {
	return public.NotFoundApplicationProblemPlusJSONResponse(
		problem(ctx, http.StatusNotFound, "ORDER_NOT_FOUND", "Заказ не найден",
			"Заказа не существует или он принадлежит другому пользователю"),
	)
}

// decodeCursor разбирает курсор страницы; признак вместо ошибки, потому что
// причина у неё одна — курсор испорчен.
func decodeCursor(cursor *string) (offset int, ok bool) {
	decoded, err := paging.DecodeOffset(value(cursor))
	if err != nil {
		return 0, false
	}

	return decoded, true
}

func orderStatuses(statuses *[]public.OrderStatus) []order.Status {
	if statuses == nil {
		return nil
	}

	converted := make([]order.Status, 0, len(*statuses))
	for _, status := range *statuses {
		converted = append(converted, order.Status(status))
	}

	return converted
}

// trimPage отрезает лишнюю запись, взятую ради признака «есть ещё страница».
func trimPage(orders []order.Order, offset, limit int) (page []order.Order, nextCursor string) {
	if len(orders) <= limit {
		return orders, ""
	}

	return orders[:limit], paging.EncodeOffset(offset + limit)
}

func deref[T any](ptr *T) T {
	var zero T

	if ptr == nil {
		return zero
	}

	return *ptr
}
