package integration_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSchemaAndSeedApplied — страховка каркаса: если контейнер поднялся,
// но миграции или сид не доехали, все остальные тесты падали бы с невнятными
// ошибками про отсутствующие таблицы.
func TestSchemaAndSeedApplied(t *testing.T) {
	ctx := testContext(t)

	var tables int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version'`).Scan(&tables))
	require.Equal(t, 20, tables, "схема накатана не полностью")

	var venues, items int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM venues), (SELECT count(*) FROM menu_items)`).
		Scan(&venues, &items))
	require.Equal(t, 3, venues)
	require.Equal(t, 26, items)
}
