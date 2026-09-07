package workflow_test

import (
	"github.com/emotionlessdev/avito-kitchen/internal/order"
	orderworkflow "github.com/emotionlessdev/avito-kitchen/internal/order/workflow"

	"github.com/stretchr/testify/mock"
)

// mockAny — любой аргумент. Отдельное имя вместо mock.Anything по всему файлу:
// в ожиданиях активностей важен не контекст, а переход статуса.
var mockAny = mock.Anything

// mockStatus сопоставляет вызов SetStatus с ожидаемым переходом: так тест
// падает с понятным сообщением, а не «ожидался вызов, получен другой».
func mockStatus(to order.Status, actor order.Actor) any {
	return mock.MatchedBy(func(input orderworkflow.SetStatusInput) bool {
		return input.To == to && input.Actor == actor
	})
}
