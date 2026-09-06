// Package seed наполняет базу демо-данными стенда: три заведения грузинской
// кухни с меню, расписанием и ключами Merchant API, плюс два пользователя,
// на которых ссылаются demo.sh и e2e-тесты.
//
// Скрипт идемпотентен, поэтому его безопасно гонять на непустой базе:
// повторный запуск обновляет демо-строки, а не создаёт вторые.
package seed

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed data.sql
var script string

// Apply выполняет скрипт демо-данных одним запросом. Запрос без аргументов
// идёт простым протоколом, поэтому все команды файла применяются в одной
// неявной транзакции: частично наполненной базы после сбоя не остаётся.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, script); err != nil {
		return fmt.Errorf("демо-данные: %w", err)
	}

	return nil
}
