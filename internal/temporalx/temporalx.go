// Package temporalx — подключение к Temporal.
//
// Temporal владеет процессом одного заказа: таймерами, ожиданием сигналов
// и компенсациями. Состояние он не хранит — источник истины остаётся
// в Postgres (главный ADR, docs/DESIGN.md §5), поэтому недоступность
// Temporal ломает продвижение заказов, но не выдачу каталога.
package temporalx

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/config"

	"go.temporal.io/sdk/client"
)

// Connect подключается к Temporal, дожидаясь его готовности.
//
// Ожидание нужно стенду: auto-setup накатывает свои схемы около сорока
// секунд, и сервис, стартовавший рядом, обязан это пережить, а не упасть.
func Connect(ctx context.Context, cfg config.Temporal, log *slog.Logger) (client.Client, error) {
	for attempt := 1; ; attempt++ {
		temporal, err := client.Dial(client.Options{
			HostPort:  cfg.HostPort,
			Namespace: cfg.Namespace,
			Logger:    NewLogger(log),
		})
		if err == nil {
			return temporal, nil
		}

		log.WarnContext(ctx, "Temporal ещё не отвечает",
			slog.Int("attempt", attempt),
			slog.String("error", err.Error()))

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("ожидание Temporal прервано: %w", ctx.Err())
		case <-time.After(cfg.ConnectRetryInterval):
		}
	}
}

// HealthCheck — проверка готовности для /readyz.
func HealthCheck(temporal client.Client, namespace string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if _, err := temporal.WorkflowService().GetSystemInfo(ctx, systemInfoRequest()); err != nil {
			return fmt.Errorf("движок недоступен: %w", err)
		}

		_ = namespace

		return nil
	}
}
