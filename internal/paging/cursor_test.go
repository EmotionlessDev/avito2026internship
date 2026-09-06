package paging_test

import (
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/paging"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeOffset(t *testing.T) {
	t.Parallel()

	for _, offset := range []int{0, 1, 20, 1000} {
		got, err := paging.DecodeOffset(paging.EncodeOffset(offset))
		require.NoError(t, err)
		assert.Equal(t, offset, got)
	}
}

func TestDecodeOffsetRejectsGarbage(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"не base64":     "!!!",
		"чужой формат":  "aGVsbG8",
		"отрицательное": "bzE6LTE",
		"не число":      "bzE6YWJj",
	}

	for name, cursor := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := paging.DecodeOffset(cursor)
			require.ErrorIs(t, err, paging.ErrBadCursor)
		})
	}
}

func TestDecodeOffsetEmptyIsStart(t *testing.T) {
	t.Parallel()

	offset, err := paging.DecodeOffset("")
	require.NoError(t, err)
	assert.Equal(t, 0, offset)
}

func TestPage(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4, 5}

	t.Run("первая страница отдаёт курсор", func(t *testing.T) {
		t.Parallel()

		got, next := paging.Page(items, 0, 2)
		assert.Equal(t, []int{1, 2}, got)
		require.NotEmpty(t, next)

		offset, err := paging.DecodeOffset(next)
		require.NoError(t, err)
		assert.Equal(t, 2, offset)
	})

	t.Run("последняя страница курсора не отдаёт", func(t *testing.T) {
		t.Parallel()

		got, next := paging.Page(items, 4, 2)
		assert.Equal(t, []int{5}, got)
		assert.Empty(t, next)
	})

	t.Run("смещение за пределами выдачи", func(t *testing.T) {
		t.Parallel()

		got, next := paging.Page(items, 99, 2)
		assert.Empty(t, got)
		assert.Empty(t, next)
	})
}
