package pos

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Подпись — единственное, что отличает вызов платформы от постороннего
// запроса на открытый порт заведения. Поэтому её проверка тестируется
// отдельно от всего остального.
func TestValidSignature(t *testing.T) {
	t.Parallel()

	const secret = "pos-demo-secret-0123456789"

	service := &Service{cfg: Config{WebhookSecret: secret}}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	body := []byte(`{"event_id":"01890000-0000-7000-8000-000000000001"}`)

	sign := func(secret, timestamp string, body []byte) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(timestamp))
		mac.Write([]byte("."))
		mac.Write(body)

		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}

	assert.True(t, service.validSignature(sign(secret, timestamp, body), timestamp, body),
		"подпись платформы принимается")

	assert.False(t, service.validSignature(sign("чужой секрет", timestamp, body), timestamp, body),
		"подпись чужим секретом отвергается")

	assert.False(t, service.validSignature(sign(secret, timestamp, body), timestamp, []byte(`{}`)),
		"подменённое тело ломает подпись")

	assert.False(t, service.validSignature(sign(secret, "2020-01-01T00:00:00Z", body), timestamp, body),
		"подпись, снятая с другого времени, не подходит")

	assert.False(t, service.validSignature("", timestamp, body), "пустая подпись не проходит")
}

// Сценарные триггеры одноразовые: «следующий заказ» — ровно один, иначе
// демо после первого отказа отклоняло бы всё подряд.
func TestScenarioDecisionIsSingleUse(t *testing.T) {
	t.Parallel()

	service := &Service{}
	service.scenario.rejectNext = true

	reject, ignore := service.takeDecision()
	assert.True(t, reject)
	assert.False(t, ignore)

	reject, ignore = service.takeDecision()
	assert.False(t, reject, "триггер сработал один раз и сбросился")
	assert.False(t, ignore)
}

func TestOffline(t *testing.T) {
	t.Parallel()

	service := &Service{}
	assert.False(t, service.offline(), "по умолчанию заведение на связи")

	service.scenario.offlineUntil = time.Now().Add(time.Minute)
	assert.True(t, service.offline())

	service.scenario.offlineUntil = time.Now().Add(-time.Minute)
	assert.False(t, service.offline(), "срок недоступности истёк")
}

// ETA в контракте — целые минуты, а демо-готовка занимает секунды: округление
// вниз дало бы ноль, а такой ETA платформа не примет.
func TestETAMinutes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 1, etaMinutes(Config{PrepTime: 10 * time.Second}))
	assert.Equal(t, 1, etaMinutes(Config{PrepTime: time.Minute}))
	assert.Equal(t, 35, etaMinutes(Config{PrepTime: 35 * time.Minute}))
}

// Модификаторы обязаны ехать вместе с позицией: снапшот полный, и всё,
// чего в нём нет, платформа считает исчезнувшим.
func TestSnapshotGroups(t *testing.T) {
	t.Parallel()

	groups := []menuOptionGroup{
		{
			ItemExternalID: "hv-001", ExternalID: "hv-001-size", Name: "Размер", MaxChoices: 1,
			Options: []menuOption{{ExternalID: "hv-001-size-l", Name: "Большая", PriceDeltaMinor: 15000}},
		},
		{ItemExternalID: "hv-004", ExternalID: "hv-004-sauce", Name: "Соус"},
	}

	own := snapshotGroups(groups, "hv-001")
	assert.Len(t, own, 1)
	assert.Equal(t, "hv-001-size", own[0].ExternalId)
	assert.Len(t, *own[0].Options, 1)

	assert.Empty(t, snapshotGroups(groups, "hv-009"), "у позиции без модификаторов их и нет")
}
