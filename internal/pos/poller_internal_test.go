package pos

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Первый тик читает ленту целиком, дальше — только изменения: иначе
// поллинг-фолбэк перечитывал бы весь список заказов каждые пять секунд.
func TestPollSinceStartsEmptyThenFollowsLastTick(t *testing.T) {
	t.Parallel()

	service := &Service{}

	require.Nil(t, service.pollSince(), "на первом тике отсечки быть не может")

	tick := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	service.rememberPoll(tick)

	since := service.pollSince()
	require.NotNil(t, since)

	// Отсечка сдвинута назад: заказ, попавший в ленту ровно в момент тика,
	// не должен провалиться между двумя запросами.
	assert.Equal(t, tick.Add(-pollOverlap), *since)
	assert.True(t, since.Before(tick))
}

// Перезапуск теряет отсечку — и это осознанно: заведение прочитает ленту
// целиком один раз, а приём заказов идемпотентен по идентификатору платформы.
func TestPollSinceResetsWithService(t *testing.T) {
	t.Parallel()

	service := &Service{}
	service.rememberPoll(time.Now().UTC())
	require.NotNil(t, service.pollSince())

	restarted := &Service{}
	assert.Nil(t, restarted.pollSince())
}
