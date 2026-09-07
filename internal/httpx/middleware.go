package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/logger"

	"github.com/google/uuid"
)

// HeaderRequestID — сквозной идентификатор запроса. Клиент может прислать свой
// (тогда трассировка склеится с вызывающей системой), иначе генерируем сами.
const HeaderRequestID = "X-Request-Id"

type requestIDKey struct{}

// RequestID проставляет request-id в контекст, в логгер и в заголовок ответа.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if id == "" {
			id = newRequestID()
		}

		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		ctx = logger.Into(ctx, logger.From(ctx).With(slog.String("request_id", id)))
		w.Header().Set(HeaderRequestID, id)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFrom достаёт request-id из контекста; пустая строка — если middleware
// не отработал (например, в юнит-тесте хендлера).
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)

	return id
}

// newRequestID отдаёт UUID v7: он сортируется по времени, поэтому в логах
// запросы упорядочены даже при перемешанном выводе нескольких контейнеров.
func newRequestID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}

	return uuid.NewString()
}

type pathKey struct{}

// RequestPath кладёт путь запроса в контекст.
//
// Нужен ошибкам: поле instance в problem+json обязано указывать на ресурс,
// а хендлеры strict-сервера получают только контекст — самого запроса
// у них уже нет.
func RequestPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), pathKey{}, r.URL.Path)))
	})
}

// PathFrom достаёт путь запроса из контекста; пустая строка — если middleware
// не отработал.
func PathFrom(ctx context.Context) string {
	path, _ := ctx.Value(pathKey{}).(string)

	return path
}

// AccessLog пишет одну строку на завершённый запрос. Строка одна намеренно:
// пара «начало/конец» удваивает объём логов, не добавляя информации.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Логгер в контекст кладём здесь, чтобы хендлеры ниже писали
			// через logger.From(ctx) и не тянули *slog.Logger параметром.
			ctx := logger.Into(r.Context(), log.With(
				slog.String("request_id", RequestIDFrom(r.Context())),
			))
			r = r.WithContext(ctx)

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			started := time.Now()

			next.ServeHTTP(rec, r)

			logger.From(ctx).LogAttrs(ctx, levelFor(rec.status), "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.written),
				slog.Duration("duration", time.Since(started)),
				slog.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}

// levelFor поднимает уровень для ошибок: 5xx должны быть видны при LOG_LEVEL=warn.
func levelFor(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// Recover превращает панику в 500 и пишет её в лог со стеком: один упавший
// хендлер не должен ронять весь процесс.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		defer func() {
			rvr := recover()
			if rvr == nil {
				return
			}

			// http.ErrAbortHandler — штатный способ оборвать ответ, не паника.
			if err, ok := rvr.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rvr)
			}

			logger.From(ctx).ErrorContext(ctx, "паника в хендлере",
				slog.Any("panic", rvr),
				slog.String("stack", string(debug.Stack())),
			)

			Internal(w, r)
		}()

		next.ServeHTTP(w, r)
	})
}

// statusRecorder запоминает код ответа и объём тела для access-лога.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}

	r.status = status
	r.wrote = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}

	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)

	return n, err //nolint:wrapcheck // прозрачная обёртка над ResponseWriter.
}

// Unwrap открывает доступ к нижележащему ResponseWriter для http.ResponseController.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Flush проталкивает буфер клиенту.
//
// Без него SSE не работает: обёртка скрывает http.Flusher нижележащего
// writer'а, приведение типа не проходит, и события копятся в буфере вместо
// того, чтобы уходить сразу.
func (r *statusRecorder) Flush() {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}

	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
