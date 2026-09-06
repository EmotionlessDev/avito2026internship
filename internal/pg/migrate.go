package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/emotionlessdev/avito-kitchen/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// MigrateCommand — операция над схемой. Значения совпадают с аргументом
// командной строки мигратора, чтобы `migrator up` читалось так же, как код.
type MigrateCommand string

// Поддерживаемые операции. Набор намеренно узкий: всё, что нужно стенду
// и проверке отката, и ничего сверх того.
const (
	MigrateUp     MigrateCommand = "up"     // накатить все непринятые миграции
	MigrateDown   MigrateCommand = "down"   // откатить одну последнюю
	MigrateReset  MigrateCommand = "reset"  // откатить все — проверка down-веток
	MigrateStatus MigrateCommand = "status" // что накачено, а что нет
)

// errUnknownCommand отделяет опечатку в аргументе от ошибки самой миграции:
// у первой лечение — исправить команду, у второй — чинить SQL.
var errUnknownCommand = errors.New("неизвестная команда мигратора")

// Migrate выполняет операцию над схемой на отдельном соединении database/sql:
// goose работает поверх стандартного интерфейса, pgx подключается через stdlib.
//
// Миграции берутся из встроенной ФС, поэтому мигратору не нужен смонтированный
// каталог — достаточно самого бинаря.
func Migrate(ctx context.Context, dsn string, cmd MigrateCommand, log *slog.Logger) error {
	db, err := openStdlib(dsn)
	if err != nil {
		return err
	}

	defer func() {
		if err := db.Close(); err != nil {
			log.WarnContext(ctx, "не удалось закрыть соединение мигратора",
				slog.String("error", err.Error()))
		}
	}()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{ctx: ctx, log: log})

	if err := goose.SetDialect(string(goose.DialectPostgres)); err != nil {
		return fmt.Errorf("диалект goose: %w", err)
	}

	if err := run(ctx, db, cmd); err != nil {
		return fmt.Errorf("миграции (%s): %w", cmd, err)
	}

	return nil
}

func run(ctx context.Context, db *sql.DB, cmd MigrateCommand) error {
	const dir = "."

	switch cmd {
	case MigrateUp:
		return goose.UpContext(ctx, db, dir)
	case MigrateDown:
		return goose.DownContext(ctx, db, dir)
	case MigrateReset:
		return goose.DownToContext(ctx, db, dir, 0)
	case MigrateStatus:
		return goose.StatusContext(ctx, db, dir)
	default:
		return fmt.Errorf("%w: %q", errUnknownCommand, cmd)
	}
}

func openStdlib(dsn string) (*sql.DB, error) {
	connCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("разбор POSTGRES_DSN: %w", err)
	}

	db := stdlib.OpenDB(*connCfg)

	// Миграции идут одна за другой, параллелизм тут не нужен: одно соединение
	// исключает случай, когда advisory-lock goose взят другим соединением пула.
	db.SetMaxOpenConns(1)

	return db, nil
}

// gooseLogger заворачивает вывод goose в общий структурный лог: иначе
// прогресс миграций уходит в stdout мимо формата остальных сервисов.
type gooseLogger struct {
	// goose.Logger не принимает контекст, поэтому логгер несёт его в себе:
	// мигратор живёт один вызов, времени этому контексту устареть негде.
	ctx context.Context
	log *slog.Logger
}

func (l gooseLogger) Printf(format string, v ...any) {
	l.log.InfoContext(l.ctx, "goose", slog.String("message", trimNewline(fmt.Sprintf(format, v...))))
}

func (l gooseLogger) Fatalf(format string, v ...any) {
	l.log.ErrorContext(l.ctx, "goose", slog.String("message", trimNewline(fmt.Sprintf(format, v...))))
}

// trimNewline снимает перевод строки: goose пишет свои сообщения готовыми
// строками терминала, а в структурном логе перевод строки внутри атрибута
// ломает разбор.
func trimNewline(s string) string {
	return strings.TrimRight(s, "\r\n")
}
