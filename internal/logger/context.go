package logger

import (
	"context"
	"log/slog"
)

type ctxKey struct{}

// Into кладёт логгер в контекст. Дальше по стеку его достают через From,
// поэтому обогащение (request-id, user-id) делается один раз в middleware.
func Into(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From достаёт логгер из контекста. Если его там нет — возвращает слоговый
// дефолт, чтобы вызывающий код никогда не разыменовывал nil.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}

	return slog.Default()
}
