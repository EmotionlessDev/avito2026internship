// Package logger настраивает структурированное логирование поверх log/slog
// и переносит логгер через context, чтобы к каждой записи автоматически
// прилипал request-id.
package logger

import (
	"log/slog"
	"os"

	"github.com/emotionlessdev/avito-kitchen/internal/config"
)

// New собирает логгер по конфигурации приложения. В dev — текстовый вывод
// для чтения глазами в `docker compose logs`, в prod — JSON для сборщика.
func New(cfg config.App) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}

	var handler slog.Handler
	if cfg.LogFormat == config.LogFormatText {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(
		slog.String("service", cfg.ServiceName),
		slog.String("env", cfg.Env),
	)
}
