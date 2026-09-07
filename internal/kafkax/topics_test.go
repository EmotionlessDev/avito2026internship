package kafkax_test

import (
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/events"
	"github.com/emotionlessdev/avito-kitchen/internal/kafkax"

	"github.com/stretchr/testify/assert"
)

// Маршрутизация событий по топикам — часть контракта: наблюдатели подписаны
// на конкретные имена, и заказ, уехавший в топик каталога, никто не прочитает.
func TestRoute(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		aggregate string
		eventType string
		want      string
	}{
		"заказ создан":    {events.AggregateOrder, events.TypeOrderCreated, kafkax.TopicOrderEvents},
		"статус заказа":   {events.AggregateOrder, events.TypeOrderStatusChanged, kafkax.TopicOrderEvents},
		"позиция меню":    {events.AggregateMenuItem, events.TypeMenuItemChanged, kafkax.TopicCatalogEvents},
		"позиция архивна": {events.AggregateMenuItem, events.TypeMenuItemArchived, kafkax.TopicCatalogEvents},
		"заведение":       {events.AggregateVenue, "venue.updated", kafkax.TopicCatalogEvents},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, kafkax.Route(tt.aggregate, tt.eventType))
		})
	}
}
