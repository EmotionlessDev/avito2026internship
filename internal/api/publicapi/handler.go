// Package publicapi реализует клиентский контур платформы поверх контракта,
// сгенерированного из api/public.openapi.yaml.
//
// Хендлеры здесь тонкие: разобрать параметры, сходить в сервис, перевести
// доменный результат в типы контракта. Вся логика — в internal/catalog
// и internal/search, поэтому её можно тестировать без HTTP.
package publicapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/catalog"
	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
	"github.com/emotionlessdev/avito-kitchen/internal/paging"
	"github.com/emotionlessdev/avito-kitchen/internal/search"
)

// Handler реализует public.StrictServerInterface.
//
// Сервис заказов добавится сюда полем на этапах 6–7; пока его ручки
// отвечают 501 — см. notImplemented.
type Handler struct {
	catalog *catalog.Service
	search  *search.Service
	cart    *cart.Service
}

// New собирает хендлер клиентского контура.
func New(catalogService *catalog.Service, searchService *search.Service, cartService *cart.Service) *Handler {
	return &Handler{catalog: catalogService, search: searchService, cart: cartService}
}

// defaultLimit повторяет default из спеки: oapi-codegen значения по умолчанию
// не подставляет, а расходиться с контрактом нельзя.
const defaultLimit = 20

// ListVenues — каталог заведений.
func (h *Handler) ListVenues(
	ctx context.Context, request public.ListVenuesRequestObject,
) (public.ListVenuesResponseObject, error) {
	params := catalog.ListParams{
		Point:   point(request.Params.Lat, request.Params.Lon),
		Query:   value(request.Params.Q),
		OpenNow: value(request.Params.OpenNow),
		Sort:    sortMode(request.Params.Sort),
		Cursor:  value(request.Params.Cursor),
		Limit:   limit(request.Params.Limit),
	}

	page, err := h.catalog.ListVenues(ctx, params)
	if err != nil {
		return listVenuesError(ctx, err)
	}

	items := make([]public.VenueSummary, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, venueSummary(page.Items[i]))
	}

	return public.ListVenues200JSONResponse{
		Items:      items,
		NextCursor: nextCursor(page.NextCursor),
	}, nil
}

// GetVenue — карточка заведения.
func (h *Handler) GetVenue(
	ctx context.Context, request public.GetVenueRequestObject,
) (public.GetVenueResponseObject, error) {
	view, err := h.catalog.GetVenue(ctx, request.VenueId, nil)
	if err != nil {
		if errors.Is(err, catalog.ErrVenueNotFound) {
			return public.GetVenue404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: venueNotFound(ctx),
			}, nil
		}

		return nil, err
	}

	return public.GetVenue200JSONResponse(venueDetails(view)), nil
}

// GetVenueMenu — меню заведения по категориям.
func (h *Handler) GetVenueMenu(
	ctx context.Context, request public.GetVenueMenuRequestObject,
) (public.GetVenueMenuResponseObject, error) {
	domain, err := h.catalog.GetMenu(ctx, request.VenueId, value(request.Params.IncludeUnavailable))
	if err != nil {
		if errors.Is(err, catalog.ErrVenueNotFound) {
			return public.GetVenueMenu404ApplicationProblemPlusJSONResponse{
				NotFoundApplicationProblemPlusJSONResponse: venueNotFound(ctx),
			}, nil
		}

		return nil, err
	}

	return public.GetVenueMenu200JSONResponse(menu(domain)), nil
}

// SearchItems — сквозной поиск блюд.
func (h *Handler) SearchItems(
	ctx context.Context, request public.SearchItemsRequestObject,
) (public.SearchItemsResponseObject, error) {
	page, err := h.search.Search(ctx, search.Query{
		Text:          request.Params.Q,
		Point:         point(request.Params.Lat, request.Params.Lon),
		OnlyAvailable: value(request.Params.OnlyAvailable),
		Cursor:        value(request.Params.Cursor),
		Limit:         limit(request.Params.Limit),
	})
	if err != nil {
		if errors.Is(err, paging.ErrBadCursor) {
			return public.SearchItems400ApplicationProblemPlusJSONResponse{
				BadRequestApplicationProblemPlusJSONResponse: badCursor(ctx),
			}, nil
		}

		return nil, err
	}

	items := make([]public.SearchItem, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, searchItem(page.Items[i]))
	}

	return public.SearchItems200JSONResponse{
		Items:      items,
		NextCursor: nextCursor(page.NextCursor),
	}, nil
}

func listVenuesError(ctx context.Context, err error) (public.ListVenuesResponseObject, error) {
	switch {
	case errors.Is(err, catalog.ErrDistanceSortWithoutPoint):
		return public.ListVenues400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: public.BadRequestApplicationProblemPlusJSONResponse(
				problem(ctx, http.StatusBadRequest, "MISSING_COORDINATES",
					"Нужны координаты", "sort=distance требует параметров lat и lon"),
			),
		}, nil
	case errors.Is(err, paging.ErrBadCursor):
		return public.ListVenues400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: badCursor(ctx),
		}, nil
	default:
		return nil, err
	}
}

func venueNotFound(ctx context.Context) public.NotFoundApplicationProblemPlusJSONResponse {
	return public.NotFoundApplicationProblemPlusJSONResponse(
		problem(ctx, http.StatusNotFound, "VENUE_NOT_FOUND", "Заведение не найдено",
			"Заведение не существует или ещё не опубликовано"),
	)
}

func badCursor(ctx context.Context) public.BadRequestApplicationProblemPlusJSONResponse {
	return public.BadRequestApplicationProblemPlusJSONResponse(
		problem(ctx, http.StatusBadRequest, "INVALID_CURSOR", "Некорректный курсор",
			"Курсор нужно брать из поля next_cursor предыдущего ответа"),
	)
}

// problem переиспользует платформенный конструктор, чтобы поле type
// выводилось из кода одинаково во всех контурах, и подставляет путь запроса
// в instance — сам запрос до strict-хендлера не доезжает.
func problem(ctx context.Context, status int, code, title, detail string) public.Problem {
	p := httpx.NewProblem(status, code, title, detail)
	p.Instance = httpx.PathFrom(ctx)

	return public.Problem{
		Type:     p.Type,
		Title:    p.Title,
		Status:   p.Status,
		Code:     p.Code,
		Detail:   &p.Detail,
		Instance: &p.Instance,
	}
}

func point(lat, lon *float64) *catalog.Point {
	// Половина координат бессмысленна: без обоих значений расстояние не считается.
	if lat == nil || lon == nil {
		return nil
	}

	return &catalog.Point{Lat: *lat, Lon: *lon}
}

func sortMode(sort *public.ListVenuesParamsSort) catalog.SortMode {
	if sort == nil {
		return catalog.SortByName
	}

	return catalog.SortMode(*sort)
}

func limit(value *int) int {
	if value == nil || *value <= 0 {
		return defaultLimit
	}

	return *value
}

func value[T any](ptr *T) T {
	if ptr == nil {
		var zero T

		return zero
	}

	return *ptr
}
