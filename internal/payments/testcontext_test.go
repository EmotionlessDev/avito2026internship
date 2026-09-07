package payments_test

import (
	"context"
	"testing"
)

// testContext — контекст, живущий ровно столько, сколько идёт тест.
//
// Заменяет testing.T.Context(): он появился в более поздней версии Go, чем
// та, на которую собирается проект. Поведение то же — по завершении теста
// контекст отменяется, и незавершённые операции не тянутся в следующий.
func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return ctx
}
