// Package integration_test проверяет репозитории на настоящем Postgres.
//
// Настоящем, а не на моке: весь смысл этих тестов в SQL, который написан
// руками, — в фильтрах мультитенантности, в блокировках, в конфликтах
// уникальных индексов и в поведении транзакций. Мок хранилища проверял бы
// только то, что мок работает.
//
// База поднимается один раз на пакет в контейнере (testcontainers-go),
// миграции и демо-данные накатываются из тех же исходников, что и на стенде.
package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/pg"
	"github.com/emotionlessdev/avito-kitchen/scripts/seed"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// pool — общий пул на весь пакет. Поднимать контейнер на каждый тест дорого:
// старт Postgres с миграциями занимает секунды, а тестов десятки.
var pool *pgxpool.Pool

// startupTimeout — сколько ждём готовности контейнера. Первый запуск тянет
// образ, поэтому запас большой.
const startupTimeout = 3 * time.Minute

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "интеграционные тесты не поднялись:", err)
		os.Exit(1)
	}

	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	container, dsn, err := startPostgres(ctx)
	if err != nil {
		return 0, err
	}

	defer func() {
		// Контейнер гасим в defer, а не в t.Cleanup: он общий на пакет.
		if terminateErr := container.Terminate(context.Background()); terminateErr != nil {
			fmt.Fprintln(os.Stderr, "контейнер не остановлен:", terminateErr)
		}
	}()

	if schemaErr := prepareSchema(ctx, dsn); schemaErr != nil {
		return 0, schemaErr
	}

	pool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		return 0, fmt.Errorf("пул соединений: %w", err)
	}

	defer pool.Close()

	if err := seed.Apply(ctx, pool); err != nil {
		return 0, fmt.Errorf("демо-данные: %w", err)
	}

	return m.Run(), nil
}

// startPostgres поднимает контейнер с той же версией и локалью, что на стенде:
// локаль важна не для красоты, а для русского полнотекста и pg_trgm.
func startPostgres(ctx context.Context) (testcontainers.Container, string, error) {
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kitchen"),
		tcpostgres.WithUsername("kitchen"),
		tcpostgres.WithPassword("kitchen"),
		testcontainers.WithEnv(map[string]string{
			"POSTGRES_INITDB_ARGS": "--locale=C.UTF-8 --encoding=UTF8",
		}),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(startupTimeout)),
	)
	if err != nil {
		return nil, "", fmt.Errorf("контейнер Postgres: %w", err)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, "", fmt.Errorf("строка подключения: %w", err)
	}

	return container, dsn, nil
}

// prepareSchema накатывает миграции тем же кодом, что и мигратор стенда.
// Если бы тесты создавали схему своим DDL, они проверяли бы вымышленную базу.
func prepareSchema(ctx context.Context, dsn string) error {
	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if err := pg.Migrate(ctx, dsn, pg.MigrateUp, quiet); err != nil {
		return fmt.Errorf("миграции: %w", err)
	}

	return nil
}

// testContext — контекст теста с автоотменой. Свой хелпер, а не t.Context():
// проект собирается под Go 1.23, где его ещё нет.
func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return ctx
}
