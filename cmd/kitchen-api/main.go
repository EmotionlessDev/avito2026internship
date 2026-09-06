// Команда kitchen-api поднимает HTTP-контуры платформы: публичный (клиент)
// и merchant (заведения). Состояние читается только из Postgres — см. главный
// ADR в docs/DESIGN.md §5.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/emotionlessdev/avito-kitchen/internal/config"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
	"github.com/emotionlessdev/avito-kitchen/internal/logger"
)

func main() {
	if err := run(); err != nil {
		// Логгер на этот момент может быть ещё не собран (упали на конфиге),
		// поэтому пишем в stderr напрямую.
		fmt.Fprintf(os.Stderr, "kitchen-api: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadKitchenAPI()
	if err != nil {
		return err
	}

	log := logger.New(cfg.App)

	// SIGTERM приходит от `docker compose down` и от оркестратора;
	// SIGINT — от Ctrl+C при локальном запуске.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	health := httpx.NewHealth()
	// Проверка Postgres регистрируется здесь на этапе 1, Temporal — на этапе 7.

	router := httpx.NewRouter(log, health)

	log.InfoContext(ctx, "запускаю kitchen-api",
		slog.String("env", cfg.Env),
		slog.String("addr", cfg.HTTP.Addr))

	if err := httpx.NewServer(cfg.HTTP, cfg.App, router, log).Run(ctx); err != nil {
		return err
	}

	log.InfoContext(ctx, "kitchen-api остановлен")

	return nil
}
