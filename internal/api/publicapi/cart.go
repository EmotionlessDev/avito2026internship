package publicapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/catalog"
	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"

	"github.com/google/uuid"
)

// Ручки корзины. В отличие от каталога они требуют пользователя: анонимной
// корзины не бывает. Пользователь берётся из контекста — заголовок разобрал
// middleware, а хендлеры к заголовкам не ходят вовсе (docs/DESIGN.md §11).

// GetCart — корзина с пересчитанными ценами.
func (h *Handler) GetCart(
	ctx context.Context, _ public.GetCartRequestObject,
) (public.GetCartResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.GetCart401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	current, err := h.cart.Get(ctx, userID)
	if err != nil {
		return nil, err
	}

	return public.GetCart200JSONResponse(cartResponse(current)), nil
}

// AddCartItem — добавить позицию.
func (h *Handler) AddCartItem(
	ctx context.Context, request public.AddCartItemRequestObject,
) (public.AddCartItemResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.AddCartItem401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	if request.Body == nil {
		return public.AddCartItem400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: public.BadRequestApplicationProblemPlusJSONResponse(
				problem(ctx, http.StatusBadRequest, httpx.CodeBadRequest,
					"Некорректный запрос", "Тело запроса обязательно"),
			),
		}, nil
	}

	updated, err := h.cart.AddItem(ctx, userID, cart.AddRequest{
		ItemID:    request.Body.ItemId,
		Qty:       request.Body.Qty,
		OptionIDs: optionIDs(request.Body.OptionIds),
	})
	if err != nil {
		return addCartItemError(ctx, err)
	}

	return public.AddCartItem200JSONResponse(cartResponse(updated)), nil
}

// UpdateCartItem — изменить количество.
func (h *Handler) UpdateCartItem(
	ctx context.Context, request public.UpdateCartItemRequestObject,
) (public.UpdateCartItemResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.UpdateCartItem401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	if request.Body == nil {
		return public.UpdateCartItem400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: public.BadRequestApplicationProblemPlusJSONResponse(
				problem(ctx, http.StatusBadRequest, httpx.CodeBadRequest,
					"Некорректный запрос", "Тело запроса обязательно"),
			),
		}, nil
	}

	updated, err := h.cart.UpdateQty(ctx, userID, request.CartItemId, request.Body.Qty)
	if err != nil {
		if errors.Is(err, cart.ErrCartItemNotFound) {
			return public.UpdateCartItem404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: cartItemNotFound(ctx),
			}, nil
		}

		return nil, err
	}

	return public.UpdateCartItem200JSONResponse(cartResponse(updated)), nil
}

// DeleteCartItem — убрать позицию.
func (h *Handler) DeleteCartItem(
	ctx context.Context, request public.DeleteCartItemRequestObject,
) (public.DeleteCartItemResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.DeleteCartItem401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	updated, err := h.cart.DeleteLine(ctx, userID, request.CartItemId)
	if err != nil {
		if errors.Is(err, cart.ErrCartItemNotFound) {
			return public.DeleteCartItem404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: cartItemNotFound(ctx),
			}, nil
		}

		return nil, err
	}

	return public.DeleteCartItem200JSONResponse(cartResponse(updated)), nil
}

// ClearCart — очистить корзину.
func (h *Handler) ClearCart(
	ctx context.Context, _ public.ClearCartRequestObject,
) (public.ClearCartResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.ClearCart401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	if err := h.cart.Clear(ctx, userID); err != nil {
		return nil, err
	}

	return public.ClearCart204Response{}, nil
}

// ValidateCart — каскад проверок перед оформлением.
func (h *Handler) ValidateCart(
	ctx context.Context, request public.ValidateCartRequestObject,
) (public.ValidateCartResponseObject, error) {
	userID, err := h.requireUser(ctx)
	if err != nil {
		return public.ValidateCart401ApplicationProblemPlusJSONResponse{
			UnauthorizedApplicationProblemPlusJSONResponse: unauthorized(ctx, err),
		}, nil
	}

	result, err := h.cart.Validate(ctx, userID, deliveryPoint(request.Body))
	if err != nil {
		if errors.Is(err, catalog.ErrVenueNotFound) {
			// Заведение сняли с публикации, пока корзина лежала. Для клиента
			// это не 404: корзина существует, просто заказать в ней нельзя.
			return public.ValidateCart200JSONResponse{IsValid: false}, nil
		}

		return nil, err
	}

	return public.ValidateCart200JSONResponse(validationResponse(result)), nil
}

// requireUser достаёт пользователя из контекста и проверяет, что платформа
// его знает.
func (h *Handler) requireUser(ctx context.Context) (uuid.UUID, error) {
	userID, ok := httpx.UserIDFrom(ctx)
	if !ok {
		return uuid.Nil, cart.ErrUnknownUser
	}

	if err := h.cart.EnsureUser(ctx, userID); err != nil {
		return uuid.Nil, err
	}

	return userID, nil
}

func addCartItemError(ctx context.Context, err error) (public.AddCartItemResponseObject, error) {
	switch {
	case errors.Is(err, cart.ErrMenuItemNotFound):
		return public.AddCartItem404ApplicationProblemPlusJSONResponse{
			NotFoundApplicationProblemPlusJSONResponse: public.NotFoundApplicationProblemPlusJSONResponse(
				problem(ctx, http.StatusNotFound, "ITEM_NOT_FOUND", "Позиция не найдена",
					"Позиция не существует, снята с продажи или удалена из меню"),
			),
		}, nil
	case errors.Is(err, cart.ErrMixedVenues):
		return public.AddCartItem409ApplicationProblemPlusJSONResponse(
			problem(ctx, http.StatusConflict, cart.CodeCartMixedVenues,
				"В корзине блюда другого заведения",
				"Очистите корзину, чтобы заказать в другом заведении"),
		), nil
	case errors.Is(err, cart.ErrInvalidOptions):
		return public.AddCartItem400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: public.BadRequestApplicationProblemPlusJSONResponse(
				problem(ctx, http.StatusBadRequest, "INVALID_OPTIONS",
					"Некорректный набор модификаторов", err.Error()),
			),
		}, nil
	default:
		return nil, err
	}
}

func unauthorized(ctx context.Context, err error) public.UnauthorizedApplicationProblemPlusJSONResponse {
	detail := "Передайте заголовок X-User-Id с идентификатором пользователя"
	if errors.Is(err, cart.ErrUnknownUser) {
		detail = "Пользователь неизвестен платформе"
	}

	return public.UnauthorizedApplicationProblemPlusJSONResponse(
		problem(ctx, http.StatusUnauthorized, "UNAUTHORIZED", "Пользователь не опознан", detail),
	)
}

func cartItemNotFound(ctx context.Context) public.NotFoundApplicationProblemPlusJSONResponse {
	return public.NotFoundApplicationProblemPlusJSONResponse(
		problem(ctx, http.StatusNotFound, "CART_ITEM_NOT_FOUND", "Позиция корзины не найдена",
			"Позиции нет в вашей корзине"),
	)
}

func optionIDs(ids *[]uuid.UUID) []uuid.UUID {
	if ids == nil {
		return nil
	}

	return *ids
}

func deliveryPoint(body *public.ValidateCartJSONRequestBody) *catalog.Point {
	if body == nil || body.Delivery == nil {
		return nil
	}

	return &catalog.Point{Lat: body.Delivery.Lat, Lon: body.Delivery.Lon}
}
