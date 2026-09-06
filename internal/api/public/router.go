package public

import (
	"log/slog"
	"net/http"

	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
	"github.com/emotionlessdev/avito-kitchen/internal/logger"

	"github.com/go-chi/chi/v5"
)

// BasePath — префикс клиентского контура; совпадает с servers[0].url в спеке.
const BasePath = "/api/v1"

// Mount навешивает клиентский контур на роутер.
//
// Роутинг берётся из сгенерированного кода: пути и методы описаны спекой,
// и повторять их руками — значит однажды разойтись с контрактом.
func Mount(router chi.Router, handler *Handler) {
	strict := public.NewStrictHandlerWithOptions(handler, nil, public.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  requestError,
		ResponseErrorHandlerFunc: responseError,
	})

	public.HandlerWithOptions(strict, public.ChiServerOptions{
		BaseURL:    BasePath,
		BaseRouter: router,
		Middlewares: []public.MiddlewareFunc{
			httpx.UserID,
		},
		ErrorHandlerFunc: requestError,
	})
}

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
