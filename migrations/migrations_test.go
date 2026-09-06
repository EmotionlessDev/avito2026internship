package migrations_test

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/migrations"

	"github.com/stretchr/testify/require"
)

// Тест держит договорённость из CLAUDE.md: в каждой миграции есть и up, и down.
// Забытая down-ветка обнаруживается здесь, а не в момент, когда откат
// понадобился на стенде.
func TestMigrationsHaveUpAndDown(t *testing.T) {
	t.Parallel()

	for _, name := range migrationFiles(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body, err := fs.ReadFile(migrations.FS, name)
			require.NoError(t, err)

			sql := string(body)
			require.Contains(t, sql, "-- +goose Up", "нет up-ветки")
			require.Contains(t, sql, "-- +goose Down", "нет down-ветки")
			require.Less(t, strings.Index(sql, "-- +goose Up"), strings.Index(sql, "-- +goose Down"),
				"down-ветка должна идти после up")
		})
	}
}

// Номера миграций обязаны идти подряд с единицы: goose применяет их по версии,
// и дыра в нумерации почти всегда означает потерянный при merge файл.
func TestMigrationVersionsAreSequential(t *testing.T) {
	t.Parallel()

	files := migrationFiles(t)
	require.NotEmpty(t, files, "миграций нет — встроенная ФС пуста")

	for i, name := range files {
		version, _, found := strings.Cut(name, "_")
		require.True(t, found, "имя миграции должно быть NNNN_описание.sql: %s", name)

		got, err := strconv.Atoi(version)
		require.NoError(t, err, "номер миграции не число: %s", name)
		require.Equal(t, i+1, got, "нумерация миграций идёт с пропуском: %s", name)
	}
}

// migrationFiles возвращает имена миграций в порядке номеров: fs.Glob сортирует
// результат, а нумерация с ведущими нулями делает лексикографический порядок
// совпадающим с числовым.
func migrationFiles(t *testing.T) []string {
	t.Helper()

	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)

	return files
}
