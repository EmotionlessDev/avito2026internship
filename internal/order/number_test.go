package order_test

import (
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/order"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatNumber(t *testing.T) {
	t.Parallel()

	moscow, err := time.LoadLocation("Europe/Moscow")
	require.NoError(t, err)

	tests := map[string]struct {
		at       time.Time
		sequence int64
		want     string
	}{
		"обычный номер": {
			at: time.Date(2026, 9, 7, 12, 0, 0, 0, moscow), sequence: 42, want: "К-260907-0042",
		},
		"первый заказ": {
			at: time.Date(2026, 9, 7, 12, 0, 0, 0, moscow), sequence: 1, want: "К-260907-0001",
		},
		"после десяти тысяч номер длиннее, но не повторяется": {
			at: time.Date(2026, 9, 7, 12, 0, 0, 0, moscow), sequence: 10001, want: "К-260907-10001",
		},
		"дата берётся из переданного момента в его зоне": {
			// 23:30 в Москве — это ещё 7 сентября, хотя в UTC уже 20:30 того же дня.
			at: time.Date(2026, 9, 7, 23, 30, 0, 0, moscow), sequence: 7, want: "К-260907-0007",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, order.FormatNumber(tt.at, tt.sequence))
		})
	}
}
