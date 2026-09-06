// Команда migrator приводит схему базы в нужное состояние и завершается.
// В compose это одноразовый сервис: остальные сервисы стартуют по его успешному
// коду возврата, поэтому мигрировать на старте каждого бинаря не нужно —
// иначе три реплики API начали бы накатывать миграции одновременно.
//
// Использование:
//
//	migrator [up|down|reset|status|seed]
//
// Без аргумента выполняется up. MIGRATOR_SEED=true добавляет к up наполнение
// демо-данными — так стенд из чистого клона поднимается уже с каталогом.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/emotionlessdev/avito-kitchen/internal/config"
	"github.com/emotionlessdev/avito-kitchen/internal/logger"
	"github.com/emotionlessdev/avito-kitchen/internal/pg"
	"github.com/emotionlessdev/avito-kitchen/scripts/seed"
)

// commandSeed стоит особняком от команд goose: это не операция над схемой,
// а наполнение данными, и живёт оно в отдельном скрипте.
const commandSeed = "seed"

var errUnknownCommand = errors.New("неизвестная команда")

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrator: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadMigrator()
	if err != nil {
		return err
	}

	log := logger.New(cfg.App)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	log.InfoContext(ctx, "запускаю migrator", slog.String("command", command))

	if err := execute(ctx, cfg, command, log); err != nil {
		return err
	}

	log.InfoContext(ctx, "migrator завершён", slog.String("command", command))

	return nil
}

func execute(ctx context.Context, cfg config.Migrator, command string, log *slog.Logger) error {
	if command == commandSeed {
		return applySeed(ctx, cfg, log)
	}

	switch pg.MigrateCommand(command) {
	case pg.MigrateUp, pg.MigrateDown, pg.MigrateReset, pg.MigrateStatus:
	default:
		return fmt.Errorf("%w: %q (ожидается up, down, reset, status или seed)", errUnknownCommand, command)
	}

	if err := pg.Migrate(ctx, cfg.Postgres.DSN, pg.MigrateCommand(command), log); err != nil {
		return err
	}

	// Сид после up — только по явному флагу: на боевой базе демо-данные не нужны.
	if command == string(pg.MigrateUp) && cfg.Seed {
		return applySeed(ctx, cfg, log)
	}

	return nil
}

func applySeed(ctx context.Context, cfg config.Migrator, log *slog.Logger) error {
	pool, err := pg.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return err
	}

	defer pool.Close()

	if err := seed.Apply(ctx, pool); err != nil {
		return err
	}

	log.InfoContext(ctx, "демо-данные загружены")

	return nil
}
