package config_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadKitchenAPI_Defaults(t *testing.T) {
	cfg, err := config.LoadKitchenAPI()
	require.NoError(t, err, "конфигурация должна собираться без единой заданной переменной")

	assert.Equal(t, config.EnvDev, cfg.Env)
	assert.Equal(t, "kitchen-api", cfg.ServiceName)
	assert.Equal(t, slog.LevelInfo, cfg.LogLevel)
	assert.Equal(t, config.LogFormatText, cfg.LogFormat, "в dev логи читаются глазами")
	assert.Equal(t, ":8080", cfg.HTTP.Addr)
	assert.Positive(t, cfg.HTTP.ReadHeaderTimeout, "таймауты должны быть заданы явно")
	assert.True(t, cfg.IsDev())
}

func TestLoadKitchenAPI_Overrides(t *testing.T) {
	t.Setenv("APP_ENV", config.EnvProd)
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("HTTP_ADDR", ":9000")
	t.Setenv("SHUTDOWN_TIMEOUT", "42s")

	cfg, err := config.LoadKitchenAPI()
	require.NoError(t, err)

	assert.Equal(t, config.EnvProd, cfg.Env)
	assert.Equal(t, slog.LevelDebug, cfg.LogLevel)
	assert.Equal(t, config.LogFormatJSON, cfg.LogFormat, "в prod логи собирает агрегатор")
	assert.Equal(t, ":9000", cfg.HTTP.Addr)
	assert.Equal(t, 42*time.Second, cfg.ShutdownTimeout)
	assert.False(t, cfg.IsDev())
}

// Опечатка в env должна валить старт с внятным сообщением, а не молча
// подставлять дефолт: сервис, поднятый не с той конфигурацией, хуже упавшего.
func TestLoadKitchenAPI_ReportsAllErrorsAtOnce(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("LOG_LEVEL", "verbose")
	t.Setenv("SHUTDOWN_TIMEOUT", "15 секунд")

	_, err := config.LoadKitchenAPI()
	require.Error(t, err)

	// Все три проблемы названы в одной ошибке — чинить их по одной не нужно.
	assert.Contains(t, err.Error(), "APP_ENV")
	assert.Contains(t, err.Error(), "LOG_LEVEL")
	assert.Contains(t, err.Error(), "SHUTDOWN_TIMEOUT")
}

func TestLoadKitchenWorker_HasOwnPort(t *testing.T) {
	cfg, err := config.LoadKitchenWorker()
	require.NoError(t, err)

	assert.Equal(t, ":8081", cfg.HTTP.Addr, "воркер и api живут в одной сети compose")
	assert.Equal(t, "kitchen-worker", cfg.ServiceName)
}

func TestLoadKitchenAPI_PostgresDefaults(t *testing.T) {
	cfg, err := config.LoadKitchenAPI()
	require.NoError(t, err)

	assert.Contains(t, cfg.Postgres.DSN, "postgres://", "дефолт указывает на стенд с хоста")
	assert.Positive(t, cfg.Postgres.MaxConns, "размер пула задаётся явно, а не дефолтом драйвера")
	assert.Positive(t, cfg.Postgres.ConnectTimeout, "старт с недоступной базой не должен висеть")
}

func TestLoadMigrator_SeedIsOptIn(t *testing.T) {
	cfg, err := config.LoadMigrator()
	require.NoError(t, err)

	assert.Equal(t, "migrator", cfg.ServiceName)
	assert.False(t, cfg.Seed, "демо-данные — явное решение стенда, а не поведение по умолчанию")
}

func TestLoadMigrator_Overrides(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://u:p@db:5432/kitchen?sslmode=disable")
	t.Setenv("POSTGRES_MAX_CONNS", "3")
	t.Setenv("MIGRATOR_SEED", "true")

	cfg, err := config.LoadMigrator()
	require.NoError(t, err)

	assert.Equal(t, "postgres://u:p@db:5432/kitchen?sslmode=disable", cfg.Postgres.DSN)
	assert.Equal(t, int32(3), cfg.Postgres.MaxConns)
	assert.True(t, cfg.Seed)
}

// Числовые и булевы переменные разбираются так же строго, как остальные:
// POSTGRES_MAX_CONNS=много должно валить старт, а не превращаться в дефолт.
func TestLoadMigrator_ReportsBadNumbersAndFlags(t *testing.T) {
	t.Setenv("POSTGRES_MAX_CONNS", "много")
	t.Setenv("MIGRATOR_SEED", "ага")

	_, err := config.LoadMigrator()
	require.Error(t, err)

	assert.Contains(t, err.Error(), "POSTGRES_MAX_CONNS")
	assert.Contains(t, err.Error(), "MIGRATOR_SEED")
}
