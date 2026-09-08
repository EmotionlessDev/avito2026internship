// Package merchantapi реализует контур заведений поверх контракта,
// сгенерированного из api/merchant.openapi.yaml.
//
// Хендлеры тонкие: перевести контракт в домен, позвать сервис, перевести
// ответ обратно. Ни один из них не подставляет venue_id — его берёт из
// контекста репозиторий, и обойти это нельзя.
package merchantapi

import (
	"context"
	"errors"
	"net/http"

	gen "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/order"
)

// Handler реализует merchant.StrictServerInterface.
//
// Хендлеры тонкие: перевести контракт в домен, позвать сервис, перевести
// ответ обратно. Статусы заказа они не пишут — шлют сигнал процессу,
// а статус проставляет активность внутри workflow.
type Handler struct {
	merchant *merchant.Service
	orders   *order.Service
}

// New собирает хендлер контура заведений.
func New(service *merchant.Service, orders *order.Service) *Handler {
	return &Handler{merchant: service, orders: orders}
}

// ImportMenu — импорт полного снапшота меню.
func (h *Handler) ImportMenu(
	ctx context.Context, request gen.ImportMenuRequestObject,
) (gen.ImportMenuResponseObject, error) {
	if request.Body == nil {
		return gen.ImportMenu400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badRequest(ctx, "Тело запроса обязательно"),
		}, nil
	}

	result, err := h.merchant.ImportMenu(ctx, snapshot(*request.Body))
	if err != nil {
		if errors.Is(err, merchant.ErrInvalidSnapshot) {
			return gen.ImportMenu422ApplicationProblemPlusJSONResponse(
				problem(ctx, http.StatusUnprocessableEntity, "INVALID_MENU_SNAPSHOT",
					"Снапшот меню противоречив", err.Error()),
			), nil
		}

		return nil, err
	}

	return gen.ImportMenu200JSONResponse(importResult(result)), nil
}

// SetAvailability — батч-переключение стоп-листа.
func (h *Handler) SetAvailability(
	ctx context.Context, request gen.SetAvailabilityRequestObject,
) (gen.SetAvailabilityResponseObject, error) {
	if request.Body == nil {
		return gen.SetAvailability400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badRequest(ctx, "Тело запроса обязательно"),
		}, nil
	}

	result, err := h.merchant.SetAvailability(ctx, availabilityRequest(*request.Body))
	if err != nil {
		return availabilityError(ctx, err)
	}

	return gen.SetAvailability200JSONResponse(availabilityResult(result)), nil
}

// UpdateVenueSettings — настройки заведения.
func (h *Handler) UpdateVenueSettings(
	ctx context.Context, request gen.UpdateVenueSettingsRequestObject,
) (gen.UpdateVenueSettingsResponseObject, error) {
	if request.Body == nil {
		return gen.UpdateVenueSettings400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badRequest(ctx, "Тело запроса обязательно"),
		}, nil
	}

	settings, err := h.merchant.UpdateVenue(ctx, venueSettings(*request.Body))
	if err != nil {
		return nil, err
	}

	return gen.UpdateVenueSettings200JSONResponse(venueSettingsResponse(settings)), nil
}

// RegisterWebhook — куда платформа шлёт заказы и чем их подписывает.
func (h *Handler) RegisterWebhook(
	ctx context.Context, request gen.RegisterWebhookRequestObject,
) (gen.RegisterWebhookResponseObject, error) {
	if request.Body == nil {
		return gen.RegisterWebhook400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badRequest(ctx, "Тело запроса обязательно"),
		}, nil
	}

	saved, err := h.merchant.RegisterWebhook(ctx, merchant.Webhook{
		URL:    request.Body.Url,
		Secret: request.Body.Secret,
	})
	if err != nil {
		return nil, err
	}

	return gen.RegisterWebhook200JSONResponse{
		Url:      saved.URL,
		IsActive: saved.IsActive,
	}, nil
}

func availabilityError(ctx context.Context, err error) (gen.SetAvailabilityResponseObject, error) {
	switch {
	case errors.Is(err, merchant.ErrEmptyAvailability):
		return gen.SetAvailability400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badRequest(ctx,
				"Передайте хотя бы одну позицию или опцию"),
		}, nil
	case errors.Is(err, merchant.ErrScheduledReturnUnavailable):
		// 400, а не 501: ручка работает, не поддержано конкретное поле.
		// Заведение может снять позицию без until и вернуть её руками.
		return gen.SetAvailability400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(
				problem(ctx, http.StatusBadRequest, "SCHEDULED_RETURN_NOT_AVAILABLE",
					"Авто-возврат пока недоступен",
					"Планировщик авто-возврата недоступен — вероятно, "+
						"не поднялся Temporal. Снимите позицию без until "+
						"и верните её вручную."),
			),
		}, nil
	default:
		return nil, err
	}
}

func badRequest(ctx context.Context, detail string) gen.BadRequestApplicationProblemPlusJSONResponse {
	return gen.BadRequestApplicationProblemPlusJSONResponse(
		problem(ctx, http.StatusBadRequest, httpx.CodeBadRequest, "Некорректный запрос", detail),
	)
}

// problem собирает тело ошибки контракта тем же конструктором, что и остальная
// платформа: поле type выводится из кода, instance — из пути запроса.
func problem(ctx context.Context, status int, code, title, detail string) gen.Problem {
	p := httpx.NewProblem(status, code, title, detail)
	p.Instance = httpx.PathFrom(ctx)

	return gen.Problem{
		Type:     p.Type,
		Title:    p.Title,
		Status:   p.Status,
		Code:     p.Code,
		Detail:   &p.Detail,
		Instance: &p.Instance,
	}
}
