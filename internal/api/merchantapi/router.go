package merchantapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	gen "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
	"github.com/emotionlessdev/avito-kitchen/internal/logger"
	"github.com/emotionlessdev/avito-kitchen/internal/merchant"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
)

// BasePath — префикс контура заведений; совпадает с servers[0].url в спеке.
const BasePath = "/api/merchant/v1"

// Mount навешивает контур заведений на роутер.
//
// Порядок middleware важен и неочевиден: сгенерированный код оборачивает
// хендлер списком по порядку, поэтому ПОСЛЕДНИЙ в списке оказывается
// снаружи и выполняется первым. Аутентификация обязана быть снаружи:
// запрос без ключа должен получить 401, а не подробности о том, чем
// его тело не понравилось валидатору.
func Mount(router chi.Router, handler *Handler, service *merchant.Service) error {
	validator, err := requestValidator()
	if err != nil {
		return err
	}

	strict := gen.NewStrictHandlerWithOptions(handler, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  requestError,
		ResponseErrorHandlerFunc: responseError,
	})

	gen.HandlerWithOptions(strict, gen.ChiServerOptions{
		BaseURL:    BasePath,
		BaseRouter: router,
		Middlewares: []gen.MiddlewareFunc{
			validator,
			Authenticator(service),
		},
		ErrorHandlerFunc: requestError,
	})

	return nil
}

// Authenticator проверяет API-ключ и кладёт заведение в контекст.
//
// Ключ сверяется по sha256: в базе лежит только отпечаток, поэтому утечка
// дампа не даёт доступа к контуру. Заведение дальше нигде не передаётся
// параметром — его достаёт из контекста репозиторий.
func Authenticator(service *merchant.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := merchant.BearerToken(r.Header.Get("Authorization"))
			if !ok {
				unauthorized(w, r, "Ожидается заголовок Authorization: Bearer <api-key>")

				return
			}

			venueID, err := service.Authenticate(r.Context(), key)
			if err != nil {
				if errors.Is(err, merchant.ErrUnauthorized) {
					unauthorized(w, r, "Ключ неизвестен, отозван или принадлежит неактивному заведению")

					return
				}

				logger.From(r.Context()).ErrorContext(r.Context(), "проверка API-ключа не удалась",
					slog.String("error", err.Error()))
				httpx.Internal(w, r)

				return
			}

			ctx := merchant.WithTenant(r.Context(), venueID)
			ctx = logger.Into(ctx, logger.From(ctx).With(slog.String("venue_id", venueID.String())))

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func unauthorized(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.WriteProblem(w, r, httpx.NewProblem(
		http.StatusUnauthorized, "UNAUTHORIZED", "Ключ не принят", detail))
}

// requestValidator проверяет запрос по спеке контура: обязательные поля
// снапшота, границы eta_minutes, длину секрета вебхука.
func requestValidator() (gen.MiddlewareFunc, error) {
	spec, err := gen.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("встроенная спека merchant: %w", err)
	}

	spec.Servers = openapi3.Servers{{URL: BasePath}}

	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			// Ключ уже проверен middleware выше — второй раз разбирать
			// заголовок незачем.
			AuthenticationFunc: func(_ context.Context, _ *openapi3filter.AuthenticationInput) error {
				return nil
			},
		},
		ErrorHandlerWithOpts: func(
			_ context.Context, err error, w http.ResponseWriter, r *http.Request,
			_ nethttpmiddleware.ErrorHandlerOpts,
		) {
			httpx.WriteProblem(w, r, httpx.NewProblem(
				http.StatusBadRequest, "REQUEST_VALIDATION_FAILED",
				"Запрос не соответствует контракту", err.Error()))
		},
	}), nil
}

func requestError(w http.ResponseWriter, r *http.Request, err error) {
	httpx.BadRequest(w, r, err.Error())
}

// responseError — ошибка, которую хендлер не смог превратить в ответ контракта.
// Наружу общий 500, подробности — в лог с request-id и venue_id.
func responseError(w http.ResponseWriter, r *http.Request, err error) {
	logger.From(r.Context()).ErrorContext(r.Context(), "ошибка обработки запроса заведения",
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()))

	httpx.Internal(w, r)
}
