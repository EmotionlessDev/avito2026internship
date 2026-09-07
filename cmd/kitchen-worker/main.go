// Команда kitchen-worker — фоновая половина платформы: Temporal worker,
// Kafka-консьюмеры и outbox-relay. Отдельный бинарь от kitchen-api потому,
// что у них разный профиль нагрузки и разные причины падать (DESIGN.md §4.2).
//
// Сейчас воркер держит outbox-relay и двух консьюмеров проекций
// (витрина поиска и уведомления). Temporal-worker добавится на этапе 7.
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
	"github.com/emotionlessdev/avito-kitchen/internal/kafkax"
	"github.com/emotionlessdev/avito-kitchen/internal/logger"
	"github.com/emotionlessdev/avito-kitchen/internal/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/notify"
	"github.com/emotionlessdev/avito-kitchen/internal/order"
	orderworkflow "github.com/emotionlessdev/avito-kitchen/internal/order/workflow"
	"github.com/emotionlessdev/avito-kitchen/internal/outbox"
	"github.com/emotionlessdev/avito-kitchen/internal/payments"
	"github.com/emotionlessdev/avito-kitchen/internal/pg"
	"github.com/emotionlessdev/avito-kitchen/internal/search"
	"github.com/emotionlessdev/avito-kitchen/internal/temporalx"

	"go.temporal.io/sdk/worker"
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

	temporal, err := temporalx.Connect(ctx, cfg.Temporal, log)
	if err != nil {
		return err
	}

	defer temporal.Close()

	health := httpx.NewHealth()
	health.Register("postgres", pg.HealthCheck(pool))
	health.Register("temporal", temporalx.HealthCheck(temporal, cfg.Temporal.Namespace))

	router := httpx.NewRouter(log, health)

	log.InfoContext(ctx, "запускаю kitchen-worker",
		slog.String("env", cfg.Env),
		slog.String("addr", cfg.HTTP.Addr))

	// Топики заводит воркер: автосоздание в брокере выключено, чтобы опечатка
	// в имени не рождала новый топик. Операция идемпотентна.
	if kafkaErr := kafkax.WaitReady(ctx, cfg.Kafka, log); kafkaErr != nil {
		return kafkaErr
	}

	if topicsErr := kafkax.EnsureTopics(ctx, cfg.Kafka, log); topicsErr != nil {
		return topicsErr
	}

	producer, err := kafkax.NewProducer(cfg.Kafka)
	if err != nil {
		return err
	}

	defer producer.Close()

	// errgroup держит несколько долгоживущих компонентов: health-сервер,
	// outbox-relay и консьюмеров проекций. Падение любого из них отменяет
	// общий контекст и гасит остальные — воркер без relay бесполезен.
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return httpx.NewServer(cfg.HTTP, cfg.App, router, log).Run(ctx)
	})

	relay := outbox.NewRelay(pool, producer, kafkax.Route, kafkax.TopicDeadLetter, outbox.Config{
		BatchSize:    cfg.Outbox.BatchSize,
		PollInterval: cfg.Outbox.PollInterval,
		MaxAttempts:  cfg.Outbox.MaxAttempts,
	}, log)

	g.Go(func() error { return relay.Run(ctx) })

	indexer, err := kafkax.NewConsumer(
		cfg.Kafka, kafkax.GroupSearchIndexer, []string{kafkax.TopicCatalogEvents},
		search.NewIndexer(pool, log), producer, log)
	if err != nil {
		return err
	}

	g.Go(func() error { return indexer.Run(ctx) })

	notifier, err := kafkax.NewConsumer(
		cfg.Kafka, kafkax.GroupNotifier, []string{kafkax.TopicOrderEvents},
		notify.NewNotifier(pool, log), producer, log)
	if err != nil {
		return err
	}

	g.Go(func() error { return notifier.Run(ctx) })

	// Temporal-worker: процессы заказов и стоп-листов. Активности держат
	// подключение к базе и платёжному провайдеру — сам workflow к ним
	// не ходит, ему нельзя.
	orderRepo := order.NewRepository(pool)
	process := orderworkflow.NewProcess(temporal, cfg.Temporal.TaskQueue)

	temporalWorker := worker.New(temporal, cfg.Temporal.TaskQueue, worker.Options{})
	temporalWorker.RegisterWorkflow(orderworkflow.OrderWorkflow)
	temporalWorker.RegisterWorkflow(orderworkflow.StopListWorkflow)
	temporalWorker.RegisterActivity(orderworkflow.NewActivities(orderRepo, payments.NewMock()))
	temporalWorker.RegisterActivity(orderworkflow.NewStopListActivities(merchant.NewRepository(pool)))

	g.Go(func() error {
		defer temporalWorker.Stop()

		if startErr := temporalWorker.Start(); startErr != nil {
			return fmt.Errorf("запуск Temporal worker: %w", startErr)
		}

		log.InfoContext(ctx, "Temporal worker запущен",
			slog.String("task_queue", cfg.Temporal.TaskQueue))

		<-ctx.Done()

		return nil
	})

	// Страховка на запуск процесса: если синхронный вызов из API не удался,
	// процесс поднимется по событию оформления.
	starter, starterErr := kafkax.NewConsumer(
		cfg.Kafka, kafkax.GroupWorkflowStarter, []string{kafkax.TopicOrderEvents},
		orderworkflow.NewStarter(process, log), producer, log)
	if starterErr != nil {
		return starterErr
	}

	g.Go(func() error { return starter.Run(ctx) })

	if err := g.Wait(); err != nil {
		return err
	}

	log.InfoContext(ctx, "kitchen-worker остановлен")

	return nil
}
