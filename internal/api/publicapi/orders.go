package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if errors.Is(err, order.ErrIdempotencyConflict) {
		return public.CreateOrder409ApplicationProblemPlusJSONResponse(
			problem(ctx, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT",
				"Ключ идемпотентности уже использован",
				"Тем же ключом оформлен другой заказ: повторите с новым Idempotency-Key"),
		), nil
	}

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

// CancelOrder — отмена заказа пользователем.
func (h *Handler) CancelOrder(
	ctx context.Context, request public.CancelOrderRequestObject,
) (public.CancelOrderResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.CancelOrder401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	reason := ""
	if request.Body != nil && request.Body.Reason != nil {
		reason = *request.Body.Reason
	}

	cancelled, err := h.orders.Cancel(ctx, userID, request.OrderId, reason)
	if err != nil {
		return cancelOrderError(ctx, err)
	}

	// Статус проставит активность workflow: хендлер только отправил сигнал,
	// поэтому в ответе заказ ещё может быть в прежнем статусе. Живой статус
	// клиент видит через SSE или повторным GET.
	return public.CancelOrder200JSONResponse(orderResponse(cancelled)), nil
}

// StreamOrderEvents — живой поток статусов заказа.
func (h *Handler) StreamOrderEvents(
	ctx context.Context, request public.StreamOrderEventsRequestObject,
) (public.StreamOrderEventsResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.StreamOrderEvents401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	events, err := h.orders.Stream(ctx, userID, request.OrderId, order.DefaultStreamOptions())
	if err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return public.StreamOrderEvents404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: orderNotFound(ctx),
			}, nil
		}

		return nil, err
	}

	return public.StreamOrderEvents200TexteventStreamResponse{
		Body: sseReader(ctx, events),
	}, nil
}

// sseReader превращает канал переходов в тело SSE-ответа.
//
// io.Pipe, а не буфер: события появляются по ходу жизни заказа, и ответ
// обязан уходить клиенту по мере их появления, а не целиком в конце.
func sseReader(ctx context.Context, events <-chan order.StatusChange) io.Reader {
	reader, writer := io.Pipe()

	go func() {
		defer func() { _ = writer.Close() }()

		for event := range events {
			payload, err := json.Marshal(statusEvent(event))
			if err != nil {
				return
			}

			if _, err := fmt.Fprintf(writer, "event: status\ndata: %s\n\n", payload); err != nil {
				return
			}

			if ctx.Err() != nil {
				return
			}
		}
	}()

	return reader
}

func cancelOrderError(ctx context.Context, err error) (public.CancelOrderResponseObject, error) {
	var notCancellable order.ErrNotCancellable
	if errors.As(err, &notCancellable) {
		return public.CancelOrder409ApplicationProblemPlusJSONResponse(
			problem(ctx, http.StatusConflict, "ORDER_NOT_CANCELLABLE",
				"Заказ уже нельзя отменить",
				"Заказ в статусе "+string(notCancellable.Status)+
					" — заведение уже приготовило блюда"),
		), nil
	}

	if errors.Is(err, order.ErrNotFound) {
		return public.CancelOrder404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: orderNotFound(ctx),
		}, nil
	}

	return nil, err
}
