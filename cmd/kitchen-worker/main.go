// Команда kitchen-worker — фоновая половина платформы: Temporal worker,
// Kafka-консьюмеры и outbox-relay. Отдельный бинарь от kitchen-api потому,
// что у них разный профиль нагрузки и разные причины падать (DESIGN.md §4.2).
//
// На этапе 0 воркер поднимает только health-сервер: Kafka подключается
// на этапе 6, Temporal — на этапе 7.
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
	"github.com/emotionlessdev/avito-kitchen/internal/pg"

	"golang.org/x/sync/errgroup"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "kitchen-worker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadKitchenWorker()
	if err != nil {
		return err
	}

	log := logger.New(cfg.App)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	pool, err := pg.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return err
	}

	defer pool.Close()

	health := httpx.NewHealth()
	health.Register("postgres", pg.HealthCheck(pool))
	// Проверки Kafka и Temporal регистрируются здесь на этапах 6 и 7.

	router := httpx.NewRouter(log, health)

	log.InfoContext(ctx, "запускаю kitchen-worker",
		slog.String("env", cfg.Env),
		slog.String("addr", cfg.HTTP.Addr))

	// errgroup держит несколько долгоживущих компонентов: сейчас только
	// health-сервер, дальше к нему добавятся worker, консьюмеры и relay.
	// Падение любого из них отменяет общий контекст и гасит остальные.
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return httpx.NewServer(cfg.HTTP, cfg.App, router, log).Run(ctx)
	})

	if err := g.Wait(); err != nil {
		return err
	}

	log.InfoContext(ctx, "kitchen-worker остановлен")

	return nil
}
