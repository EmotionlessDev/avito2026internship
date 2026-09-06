package search_test

import (
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/search"

	"github.com/stretchr/testify/assert"
)

// Ранжирование — продуктовое правило, а не деталь SQL: проверяем его
// свойствами, а не конкретными числами. Числа поменяются при подкрутке
// коэффициентов, свойства — нет.
func TestScore(t *testing.T) {
	t.Parallel()

	const relevance = 0.5

	distance := func(m int) *int { return &m }

	t.Run("доступное блюдо выше недоступного", func(t *testing.T) {
		t.Parallel()

		available := search.Score(relevance, true, true, true, nil)
		unavailable := search.Score(relevance, false, true, true, nil)

		assert.Greater(t, available, unavailable)
		assert.Positive(t, unavailable, "недоступное опускается вниз, но остаётся в выдаче")
	})

	t.Run("открытое заведение выше закрытого", func(t *testing.T) {
		t.Parallel()

		assert.Greater(t,
			search.Score(relevance, true, true, true, nil),
			search.Score(relevance, true, false, true, nil))
	})

	t.Run("принимающее заказы заведение выше стоящего на паузе", func(t *testing.T) {
		t.Parallel()

		assert.Greater(t,
			search.Score(relevance, true, true, true, nil),
			search.Score(relevance, true, true, false, nil))
	})

	t.Run("ближнее выше дальнего", func(t *testing.T) {
		t.Parallel()

		assert.Greater(t,
			search.Score(relevance, true, true, true, distance(500)),
			search.Score(relevance, true, true, true, distance(9000)))
	})

	t.Run("без координат близость не влияет", func(t *testing.T) {
		t.Parallel()

		assert.InDelta(t, relevance, search.Score(relevance, true, true, true, nil), 1e-9)
	})

	t.Run("более точное совпадение выше при прочих равных", func(t *testing.T) {
		t.Parallel()

		assert.Greater(t,
			search.Score(0.9, true, true, true, distance(1000)),
			search.Score(0.1, true, true, true, distance(1000)))
	})

	t.Run("недоступное рядом не обгоняет доступное того же качества", func(t *testing.T) {
		t.Parallel()

		assert.Greater(t,
			search.Score(relevance, true, true, true, distance(4000)),
			search.Score(relevance, false, true, true, distance(100)))
	})
}
