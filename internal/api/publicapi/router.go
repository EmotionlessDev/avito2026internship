package publicapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
	"github.com/emotionlessdev/avito-kitchen/internal/logger"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
)

// BasePath — префикс клиентского контура; совпадает с servers[0].url в спеке.
const BasePath = "/api/v1"

// Mount навешивает клиентский контур на роутер.
//
// Роутинг берётся из сгенерированного кода: пути и методы описаны спекой,
// и повторять их руками — значит однажды разойтись с контрактом.
func Mount(router chi.Router, handler *Handler) error {
	validator, err := requestValidator()
	if err != nil {
		return err
	}

	strict := public.NewStrictHandlerWithOptions(handler, nil, public.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  requestError,
		ResponseErrorHandlerFunc: responseError,
	})

	public.HandlerWithOptions(strict, public.ChiServerOptions{
		BaseURL:    BasePath,
		BaseRouter: router,
		// Список оборачивает хендлер по порядку, поэтому последний элемент
		// выполняется первым: X-User-Id разбирается до валидации контракта.
		//
		// В отличие от контура заведений, отказать здесь middleware не может:
		// каталог и поиск работают анонимно, и пользователя спрашивают у
		// контекста только те ручки, которым он нужен. Поэтому запрос
		// без X-User-Id и с некорректным телом получает 400, а не 401 —
		// цена того, что один контур обслуживает и анонимные ручки.
		Middlewares: []public.MiddlewareFunc{
			validator,
			httpx.UserID,
		},
		ErrorHandlerFunc: requestError,
	})

	return nil
}

// requestValidator проверяет запрос по той же спеке, из которой сгенерирован
// сервер: enum'ы, границы чисел, длины строк, обязательные поля тела.
//
// Без него контракт обещал бы больше, чем проверяет код: `sort=bogus`
// молча превращался бы в сортировку по названию, а `limit=100000` —
// в запрос страницы любого размера. Валидация живёт в middleware, а не
// в каждом хендлере, потому что источник правил один — YAML.
func requestValidator() (public.MiddlewareFunc, error) {
	spec, err := public.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("встроенная спека public: %w", err)
	}

	// Валидатор ищет операцию по полному пути запроса, поэтому список серверов
	// должен содержать ровно тот префикс, на который смонтирован контур.
	// В спеке это /api/v1 — здесь мы только фиксируем, что расхождение
	// сломает валидацию, а не пройдёт незамеченным.
	spec.Servers = openapi3.Servers{{URL: BasePath}}

	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			// Аутентификацию контракта здесь не проверяем: X-User-Id —
			// заглушка MVP, и ручки, которым пользователь нужен, спрашивают
			// его сами. Иначе каталог перестал бы работать анонимно.
			AuthenticationFunc: func(_ context.Context, _ *openapi3filter.AuthenticationInput) error {
				return nil
			},
		},
		ErrorHandlerWithOpts: func(
			_ context.Context, err error, w http.ResponseWriter, r *http.Request,
			_ nethttpmiddleware.ErrorHandlerOpts,
		) {
			httpx.WriteProblem(w, r, httpx.NewProblem(
				http.StatusBadRequest, codeContractViolation,
				"Запрос не соответствует контракту", err.Error()))
		},
	}), nil
}

// codeContractViolation отличает нарушение контракта от доменных ошибок:
// клиенту нужно чинить запрос, а не данные.
const codeContractViolation = "REQUEST_VALIDATION_FAILED"

// requestError отвечает на неразобранный запрос: не тот тип параметра,
// сломанный JSON, отсутствующий обязательный параметр. Сгенерированный код
// по умолчанию пишет text/plain — контракт же обещает problem+json.
func requestError(w http.ResponseWriter, r *http.Request, err error) {
	httpx.BadRequest(w, r, err.Error())
}

// responseError — ошибка, которую хендлер не смог превратить в ответ контракта:
// упавший запрос к базе, неожиданное состояние. Наружу отдаём общий 500,
// подробности пишем в лог с request-id.
func responseError(w http.ResponseWriter, r *http.Request, err error) {
	logger.From(r.Context()).ErrorContext(r.Context(), "ошибка обработки запроса",
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()))

	httpx.Internal(w, r)
}
