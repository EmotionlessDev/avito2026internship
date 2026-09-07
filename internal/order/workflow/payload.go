package workflow

import (
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/order"

	"github.com/google/uuid"
)

// Тело вебхука повторяет api/pos-webhooks.openapi.yaml. Структуры собраны
// здесь руками, а не взяты из сгенерированного клиента, чтобы платформа
// не зависела от кода, который реализует заведение: у контракта другой
// владелец и другой темп изменений.

type posOrderCreated struct {
	EventID    uuid.UUID `json:"event_id"`
	OccurredAt time.Time `json:"occurred_at"`
	Order      posOrder  `json:"order"`
}

type posOrder struct {
	ID               uuid.UUID      `json:"id"`
	Number           string         `json:"number"`
	CreatedAt        time.Time      `json:"created_at"`
	Items            []posOrderItem `json:"items"`
	ItemsAmountMinor int64          `json:"items_amount_minor"`
	TotalAmountMinor int64          `json:"total_amount_minor"`
	DeliveryAddress  string         `json:"delivery_address"`
	ContactPhone     string         `json:"contact_phone"`
	Comment          string         `json:"comment,omitempty"`
}

type posOrderItem struct {
	ExternalItemID string           `json:"external_item_id"`
	Name           string           `json:"name"`
	Qty            int              `json:"qty"`
	PriceMinor     int64            `json:"price_minor"`
	TotalMinor     int64            `json:"total_minor"`
	Options        []posOrderOption `json:"options,omitempty"`
}

type posOrderOption struct {
	ExternalOptionID string `json:"external_option_id"`
	Name             string `json:"name"`
	PriceDeltaMinor  int64  `json:"price_delta_minor"`
}

func orderCreatedEvent(eventID uuid.UUID, current order.Order) posOrderCreated {
	items := make([]posOrderItem, 0, len(current.Items))

	for i := range current.Items {
		item := current.Items[i]

		options := make([]posOrderOption, 0, len(item.Options))
		for j := range item.Options {
			options = append(options, posOrderOption{
				ExternalOptionID: item.Options[j].ExternalID,
				Name:             item.Options[j].Name,
				PriceDeltaMinor:  item.Options[j].PriceDeltaMinor,
			})
		}

		items = append(items, posOrderItem{
			// Заказ уезжает в артикулах заведения: POS не знает наших uuid.
			ExternalItemID: item.ExternalItemID,
			Name:           item.Name,
			Qty:            item.Qty,
			PriceMinor:     item.PriceMinor,
			TotalMinor:     item.TotalMinor,
			Options:        options,
		})
	}

	return posOrderCreated{
		EventID:    eventID,
		OccurredAt: time.Now().UTC(),
		Order: posOrder{
			ID:               current.ID,
			Number:           current.Number,
			CreatedAt:        current.CreatedAt,
			Items:            items,
			ItemsAmountMinor: current.ItemsAmountMinor,
			TotalAmountMinor: current.TotalAmountMinor,
			DeliveryAddress:  current.DeliveryAddress,
			ContactPhone:     current.ContactPhone,
			Comment:          current.Comment,
		},
	}
}
