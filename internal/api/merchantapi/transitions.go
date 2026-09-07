package merchantapi

import (
	"context"
	"errors"
	"net/http"

	gen "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/order"
)

// Переходы статусов, которые инициирует заведение.
//
// Хендлеры не пишут статус: они шлют сигнал процессу заказа, а статус
// проставляет активность внутри workflow. Поэтому в ответе заказ может быть
// ещё в прежнем статусе — новый появится через доли секунды, и живым его
// видно в SSE-потоке или повторным чтением.

// AcceptOrder — принять заказ и назвать ETA.
func (h *Handler) AcceptOrder(
	ctx context.Context, request gen.AcceptOrderRequestObject,
) (gen.AcceptOrderResponseObject, error) {
	venueID, err := merchant.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}

	if request.Body == nil {
		return gen.AcceptOrder400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badRequest(ctx, "Тело запроса обязательно"),
		}, nil
	}

	accepted, err := h.orders.Accept(ctx, venueID, request.OrderId, request.Body.EtaMinutes)
	if err != nil {
		return acceptError(ctx, err)
	}

	return gen.AcceptOrder200JSONResponse(merchantOrder(accepted)), nil
}

// RejectOrder — отклонить заказ.
func (h *Handler) RejectOrder(
	ctx context.Context, request gen.RejectOrderRequestObject,
) (gen.RejectOrderResponseObject, error) {
	venueID, err := merchant.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}

	if request.Body == nil {
		return gen.RejectOrder400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badRequest(ctx, "Тело запроса обязательно"),
		}, nil
	}

	rejected, err := h.orders.Reject(ctx, venueID, request.OrderId, request.Body.Reason)
	if err != nil {
		return rejectError(ctx, err)
	}

	return gen.RejectOrder200JSONResponse(merchantOrder(rejected)), nil
}

// MarkOrderReady — заказ готов к выдаче.
func (h *Handler) MarkOrderReady(
	ctx context.Context, request gen.MarkOrderReadyRequestObject,
) (gen.MarkOrderReadyResponseObject, error) {
	venueID, err := merchant.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}

	ready, err := h.orders.Ready(ctx, venueID, request.OrderId)
	if err != nil {
		return readyError(ctx, err)
	}

	return gen.MarkOrderReady200JSONResponse(merchantOrder(ready)), nil
}

// MarkOrderDelivered — заказ доставлен; на этом шаге списывается холд.
func (h *Handler) MarkOrderDelivered(
	ctx context.Context, request gen.MarkOrderDeliveredRequestObject,
) (gen.MarkOrderDeliveredResponseObject, error) {
	venueID, err := merchant.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}

	delivered, err := h.orders.Deliver(ctx, venueID, request.OrderId)
	if err != nil {
		return deliveredError(ctx, err)
	}

	return gen.MarkOrderDelivered200JSONResponse(merchantOrder(delivered)), nil
}

// Ошибки переходов одинаковы по смыслу, но типы ответов у каждой операции
// свои — сгенерированный strict-сервер не даёт их перепутать.

func acceptError(ctx context.Context, err error) (gen.AcceptOrderResponseObject, error) {
	switch classify(err) {
	case outcomeNotFound:
		return gen.AcceptOrder404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: orderNotFound(ctx),
		}, nil
	case outcomeConflict:
		return gen.AcceptOrder409ApplicationProblemPlusJSONResponse{
			InvalidTransitionApplicationProblemPlusJSONResponse: invalidTransition(ctx, err),
		}, nil
	case outcomeUnknown:
		return nil, err
	default:
		return nil, err
	}
}

func rejectError(ctx context.Context, err error) (gen.RejectOrderResponseObject, error) {
	switch classify(err) {
	case outcomeNotFound:
		return gen.RejectOrder404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: orderNotFound(ctx),
		}, nil
	case outcomeConflict:
		return gen.RejectOrder409ApplicationProblemPlusJSONResponse{
			InvalidTransitionApplicationProblemPlusJSONResponse: invalidTransition(ctx, err),
		}, nil
	case outcomeUnknown:
		return nil, err
	default:
		return nil, err
	}
}

func readyError(ctx context.Context, err error) (gen.MarkOrderReadyResponseObject, error) {
	switch classify(err) {
	case outcomeNotFound:
		return gen.MarkOrderReady404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: orderNotFound(ctx),
		}, nil
	case outcomeConflict:
		return gen.MarkOrderReady409ApplicationProblemPlusJSONResponse{
			InvalidTransitionApplicationProblemPlusJSONResponse: invalidTransition(ctx, err),
		}, nil
	case outcomeUnknown:
		return nil, err
	default:
		return nil, err
	}
}

func deliveredError(ctx context.Context, err error) (gen.MarkOrderDeliveredResponseObject, error) {
	switch classify(err) {
	case outcomeNotFound:
		return gen.MarkOrderDelivered404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: orderNotFound(ctx),
		}, nil
	case outcomeConflict:
		return gen.MarkOrderDelivered409ApplicationProblemPlusJSONResponse{
			InvalidTransitionApplicationProblemPlusJSONResponse: invalidTransition(ctx, err),
		}, nil
	case outcomeUnknown:
		return nil, err
	default:
		return nil, err
	}
}

type transitionOutcome int

const (
	outcomeUnknown transitionOutcome = iota
	outcomeNotFound
	outcomeConflict
)

// classify переводит доменную ошибку в исход, одинаковый для всех переходов.
func classify(err error) transitionOutcome {
	if errors.Is(err, order.ErrNotFound) {
		return outcomeNotFound
	}

	var invalid order.ErrInvalidTransition
	if errors.As(err, &invalid) {
		return outcomeConflict
	}

	var forbidden order.ErrForbiddenActor
	if errors.As(err, &forbidden) {
		return outcomeConflict
	}

	return outcomeUnknown
}

func orderNotFound(ctx context.Context) gen.NotFoundApplicationProblemPlusJSONResponse {
	return gen.NotFoundApplicationProblemPlusJSONResponse(
		problem(ctx, http.StatusNotFound, "ORDER_NOT_FOUND", "Заказ не найден",
			"Заказа не существует или он принадлежит другому заведению"),
	)
}

func invalidTransition(ctx context.Context, err error) gen.InvalidTransitionApplicationProblemPlusJSONResponse {
	return gen.InvalidTransitionApplicationProblemPlusJSONResponse(
		problem(ctx, http.StatusConflict, "INVALID_ORDER_TRANSITION",
			"Недопустимый переход статуса", err.Error()),
	)
}
