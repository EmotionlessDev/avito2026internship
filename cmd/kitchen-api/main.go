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

	"github.com/emotionlessdev/avito-kitchen/internal/api/merchantapi"
	"github.com/emotionlessdev/avito-kitchen/internal/api/publicapi"
	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/catalog"
	"github.com/emotionlessdev/avito-kitchen/internal/config"
	"github.com/emotionlessdev/avito-kitchen/internal/httpx"
	"github.com/emotionlessdev/avito-kitchen/internal/logger"
	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/pg"
	"github.com/emotionlessdev/avito-kitchen/internal/search"
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

	// Пул поднимается до сервера: сервис без базы отдавать каталог не может,
	// и узнать об этом лучше на старте, а не на первом запросе.
	pool, err := pg.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return err
	}

	defer pool.Close()

	health := httpx.NewHealth()
	health.Register("postgres", pg.HealthCheck(pool))
	// Проверка Temporal регистрируется здесь на этапе 7.

	router := httpx.NewRouter(log, health)

	// Каталог и поиск: поиск спрашивает у каталога, какие заведения открыты —
	// расписание живёт там, а витрина search_items его не хранит.
	catalogService := catalog.NewService(catalog.NewRepository(pool))
	searchService := search.NewService(search.NewRepository(pool), catalogService)
	// Корзине нужен каталог: состояние заведения для каскада валидации
	// берётся оттуда, где живут расписание и геометрия.
	cartService := cart.NewService(cart.NewRepository(pool), catalogService)

	if err := publicapi.Mount(router, publicapi.New(catalogService, searchService, cartService)); err != nil {
		return err
	}

	// Планировщик стоп-листа пока отказывает: durable-таймеры появятся
	// вместе с Temporal на этапе 7, и тогда здесь поменяется одна строка.
	merchantService := merchant.NewService(
		merchant.NewRepository(pool),
		merchant.UnsupportedScheduler{},
	)

	if err := merchantapi.Mount(router, merchantapi.New(merchantService), merchantService); err != nil {
		return err
	}

	log.InfoContext(ctx, "запускаю kitchen-api",
		slog.String("env", cfg.Env),
		slog.String("addr", cfg.HTTP.Addr))

	if err := httpx.NewServer(cfg.HTTP, cfg.App, router, log).Run(ctx); err != nil {
		return err
	}

	log.InfoContext(ctx, "kitchen-api остановлен")

	return nil
}
