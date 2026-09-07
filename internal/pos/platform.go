package pos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	merchant "github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"

	"github.com/google/uuid"
)

// Platform — клиент Merchant API.
//
// Сгенерирован из того же контракта, что и сервер платформы: заведение
// не знает про её внутренние пакеты и общается по опубликованному API.
type Platform struct {
	client *merchant.ClientWithResponses
}

// NewPlatform собирает клиента платформы с авторизацией по API-ключу.
func NewPlatform(baseURL, apiKey string) (*Platform, error) {
	client, err := merchant.NewClientWithResponses(
		baseURL+"/api/merchant/v1",
		merchant.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
		merchant.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+apiKey)

			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("клиент платформы: %w", err)
	}

	return &Platform{client: client}, nil
}

// ImportMenu отправляет платформе полный снапшот меню.
func (p *Platform) ImportMenu(
	ctx context.Context, items []MenuItem, categories []menuCategory, groups []menuOptionGroup,
) error {
	snapshot := merchant.MenuSnapshot{
		Categories: make([]merchant.SnapshotCategory, 0, len(categories)),
		Items:      make([]merchant.SnapshotItem, 0, len(items)),
	}

	for i := range categories {
		position := categories[i].Position
		snapshot.Categories = append(snapshot.Categories, merchant.SnapshotCategory{
			ExternalId: categories[i].ExternalID,
			Name:       categories[i].Name,
			Position:   &position,
		})
	}

	for i := range items {
		item := items[i]
		description := item.Description
		position := item.Position
		available := item.IsAvailable

		snapshotItem := merchant.SnapshotItem{
			ExternalId:         item.ExternalID,
			CategoryExternalId: item.Category,
			Name:               item.Name,
			Description:        &description,
			PriceMinor:         item.PriceMinor,
			Position:           &position,
			IsAvailable:        &available,
		}

		if itemGroups := snapshotGroups(groups, item.ExternalID); len(itemGroups) > 0 {
			snapshotItem.OptionGroups = &itemGroups
		}

		snapshot.Items = append(snapshot.Items, snapshotItem)
	}

	response, err := p.client.ImportMenuWithResponse(ctx, snapshot)
	if err != nil {
		return fmt.Errorf("импорт меню: %w", err)
	}

	if response.StatusCode() != http.StatusOK {
		return fmt.Errorf("импорт меню: %w", statusError(response.StatusCode(), response.Body))
	}

	return nil
}

// snapshotGroups собирает модификаторы позиции для снапшота.
//
// Снапшот полный: всё, чего в нём нет, платформа считает исчезнувшим.
// Поэтому модификаторы обязаны ехать вместе с позицией, иначе первый же
// импорт стёр бы их с платформы.
func snapshotGroups(groups []menuOptionGroup, itemExternalID string) []merchant.SnapshotOptionGroup {
	var result []merchant.SnapshotOptionGroup

	for i := range groups {
		if groups[i].ItemExternalID != itemExternalID {
			continue
		}

		group := groups[i]
		minChoices, maxChoices, position := group.MinChoices, group.MaxChoices, group.Position

		options := make([]merchant.SnapshotOption, 0, len(group.Options))
		for j := range group.Options {
			option := group.Options[j]
			delta, optionPosition := option.PriceDeltaMinor, option.Position

			options = append(options, merchant.SnapshotOption{
				ExternalId:      option.ExternalID,
				Name:            option.Name,
				PriceDeltaMinor: &delta,
				Position:        &optionPosition,
			})
		}

		result = append(result, merchant.SnapshotOptionGroup{
			ExternalId: group.ExternalID,
			Name:       group.Name,
			MinChoices: &minChoices,
			MaxChoices: &maxChoices,
			Position:   &position,
			Options:    &options,
		})
	}

	return result
}

// RegisterWebhook сообщает платформе, куда слать заказы.
func (p *Platform) RegisterWebhook(ctx context.Context, url, secret string) error {
	response, err := p.client.RegisterWebhookWithResponse(ctx, merchant.WebhookRequest{
		Url:    url,
		Secret: secret,
	})
	if err != nil {
		return fmt.Errorf("регистрация вебхука: %w", err)
	}

	if response.StatusCode() != http.StatusOK {
		return fmt.Errorf("регистрация вебхука: %w", statusError(response.StatusCode(), response.Body))
	}

	return nil
}

// ErrArtifactNotFound — платформа не нашла артикул: скорее всего, опечатка
// или позиция уже исчезла из меню.
var ErrArtifactNotFound = errors.New("артикул не найден на платформе")

// SetAvailability снимает позицию с продажи или возвращает её.
//
// Платформа отвечает 200 и на неизвестный артикул, перечисляя его в not_found,
// поэтому успех проверяется по телу ответа, а не по коду.
func (p *Platform) SetAvailability(
	ctx context.Context, externalID string, available, option bool,
) error {
	change := []merchant.AvailabilityChange{{ExternalId: externalID, IsAvailable: available}}

	request := merchant.AvailabilityRequest{}
	if option {
		request.Options = &change
	} else {
		request.Items = &change
	}

	response, err := p.client.SetAvailabilityWithResponse(ctx, request)
	if err != nil {
		return fmt.Errorf("стоп-лист: %w", err)
	}

	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return fmt.Errorf("стоп-лист: %w", statusError(response.StatusCode(), response.Body))
	}

	if notFound := response.JSON200.NotFound; notFound != nil && len(*notFound) > 0 {
		return fmt.Errorf("стоп-лист %q: %w", externalID, ErrArtifactNotFound)
	}

	return nil
}

// NewOrders забирает заказы, ожидающие подтверждения.
//
// Это поллинг-фолбэк: если вебхук не дошёл, заказ всё равно попадёт на кухню
// (сценарий M6).
func (p *Platform) NewOrders(ctx context.Context) ([]merchant.MerchantOrder, error) {
	status := []merchant.OrderStatus{merchant.Placed}

	response, err := p.client.ListVenueOrdersWithResponse(ctx, &merchant.ListVenueOrdersParams{
		Status: &status,
	})
	if err != nil {
		return nil, fmt.Errorf("поллинг заказов: %w", err)
	}

	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, fmt.Errorf("поллинг заказов: %w", statusError(response.StatusCode(), response.Body))
	}

	return response.JSON200.Items, nil
}

// Accept подтверждает заказ и называет время готовности.
func (p *Platform) Accept(ctx context.Context, orderID uuid.UUID, etaMinutes int) error {
	response, err := p.client.AcceptOrderWithResponse(ctx, orderID,
		merchant.AcceptOrderJSONRequestBody{EtaMinutes: etaMinutes})
	if err != nil {
		return fmt.Errorf("подтверждение заказа: %w", err)
	}

	return expectOK(response.StatusCode(), response.Body, "подтверждение заказа")
}

// Reject отклоняет заказ.
func (p *Platform) Reject(ctx context.Context, orderID uuid.UUID, reason string) error {
	response, err := p.client.RejectOrderWithResponse(ctx, orderID,
		merchant.RejectOrderJSONRequestBody{Reason: reason})
	if err != nil {
		return fmt.Errorf("отказ от заказа: %w", err)
	}

	return expectOK(response.StatusCode(), response.Body, "отказ от заказа")
}

// Ready объявляет заказ готовым.
func (p *Platform) Ready(ctx context.Context, orderID uuid.UUID) error {
	response, err := p.client.MarkOrderReadyWithResponse(ctx, orderID)
	if err != nil {
		return fmt.Errorf("готовность заказа: %w", err)
	}

	return expectOK(response.StatusCode(), response.Body, "готовность заказа")
}

// Delivered подтверждает доставку.
func (p *Platform) Delivered(ctx context.Context, orderID uuid.UUID) error {
	response, err := p.client.MarkOrderDeliveredWithResponse(ctx, orderID)
	if err != nil {
		return fmt.Errorf("доставка заказа: %w", err)
	}

	return expectOK(response.StatusCode(), response.Body, "доставка заказа")
}

// expectOK превращает неожиданный код ответа в ошибку.
//
// 409 отдельно: платформа отвечает им, когда переход уже сделан — например,
// заказ отменён пользователем, пока повар жал «принять». Для кухни это
// не сбой, а сообщение «поезд ушёл».
func expectOK(status int, body []byte, action string) error {
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		return fmt.Errorf("%s: %w", action, ErrTransitionRejected)
	default:
		return fmt.Errorf("%s: %w", action, statusError(status, body))
	}
}

// ErrTransitionRejected — платформа не приняла переход: состояние заказа
// уже изменилось.
var ErrTransitionRejected = errUnexpectedStatus{status: http.StatusConflict}

type errUnexpectedStatus struct {
	status int
	body   string
}

func (e errUnexpectedStatus) Error() string {
	if e.body == "" {
		return fmt.Sprintf("платформа ответила %d", e.status)
	}

	return fmt.Sprintf("платформа ответила %d: %s", e.status, e.body)
}

func statusError(status int, body []byte) error {
	const maxBody = 300

	text := string(body)
	if len(text) > maxBody {
		text = text[:maxBody]
	}

	return errUnexpectedStatus{status: status, body: text}
}
