package stats

import (
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/events"

	"github.com/stretchr/testify/assert"
)

func TestDeltaForCountsOnlyTerminalFacts(t *testing.T) {
	t.Parallel()

	placed := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		payload events.OrderStatusChanged
		want    delta
	}{
		"завершён — выручка и счётчик": {
			payload: events.OrderStatusChanged{ToStatus: "completed", TotalAmountMinor: 131900},
			want:    delta{completed: 1, revenueMinor: 131900},
		},
		"отклонён": {
			payload: events.OrderStatusChanged{ToStatus: "rejected", TotalAmountMinor: 131900},
			want:    delta{rejected: 1},
		},
		"истёк": {
			payload: events.OrderStatusChanged{ToStatus: "expired"},
			want:    delta{expired: 1},
		},
		"подтверждён — время подтверждения": {
			payload: events.OrderStatusChanged{
				ToStatus:  "accepted",
				PlacedAt:  &placed,
				ChangedAt: placed.Add(46 * time.Second),
			},
			want: delta{acceptSeconds: 46, acceptCount: 1},
		},
		"подтверждён без placed_at — считать нечего": {
			payload: events.OrderStatusChanged{ToStatus: "accepted"},
			want:    delta{},
		},
		"промежуточный статус витрину не двигает": {
			payload: events.OrderStatusChanged{ToStatus: "cooking", TotalAmountMinor: 131900},
			want:    delta{},
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want, deltaFor(testCase.payload))
		})
	}
}

// Часы сервисов могут разъехаться: отрицательное время подтверждения
// не должно уезжать в витрину и делать среднее бессмысленным.
func TestDeltaForClampsNegativeAcceptTime(t *testing.T) {
	t.Parallel()

	changed := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	placed := changed.Add(5 * time.Second)

	got := deltaFor(events.OrderStatusChanged{
		ToStatus:  "accepted",
		PlacedAt:  &placed,
		ChangedAt: changed,
	})

	assert.Equal(t, delta{acceptSeconds: 0, acceptCount: 1}, got)
}
