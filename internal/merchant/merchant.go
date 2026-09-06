// Package merchant — контур заведений: аутентификация по API-ключу, импорт
// меню, стоп-листы и настройки.
//
// Главное правило пакета — изоляция арендаторов. venue_id берётся из ключа,
// кладётся middleware в контекст и подставляется в запросы на уровне
// репозитория, а не хендлера. Заведение физически не может прочитать или
// изменить чужие данные, даже если хендлер написан с ошибкой.
package merchant

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Ошибки контура. Каждая соответствует одному коду ответа: сервис не знает
// про HTTP, перевод в статусы живёт в слое API.
var (
	// ErrUnauthorized — ключ не передан, отозван или неизвестен.
	ErrUnauthorized = errors.New("ключ не принят")
	// ErrNoTenant — venue_id не попал в контекст. Это ошибка проводки:
	// репозиторий вызвали в обход middleware аутентификации.
	ErrNoTenant = errors.New("заведение не определено")
	// ErrInvalidSnapshot — снапшот меню противоречив: позиция ссылается
	// на несуществующую категорию или артикулы дублируются.
	ErrInvalidSnapshot = errors.New("некорректный снапшот меню")
	// ErrEmptyAvailability — в запросе стоп-листа нет ни позиций, ни опций.
	ErrEmptyAvailability = errors.New("пустой запрос стоп-листа")
	// ErrScheduledReturnUnavailable — попросили авто-возврат по таймеру,
	// а планировщик его пока не умеет (появится вместе с Temporal, этап 7).
	ErrScheduledReturnUnavailable = errors.New("авто-возврат из стоп-листа недоступен")
)

// Snapshot — меню заведения целиком, в его собственных артикулах.
//
// Частичных обновлений в контуре нет намеренно: diff по полному состоянию
// не зависит от порядка запросов, поэтому повторная отправка или
// перепутанный порядок вызовов не портят каталог.
type Snapshot struct {
	Categories []SnapshotCategory
	Items      []SnapshotItem
}

// SnapshotCategory — раздел меню в снапшоте.
type SnapshotCategory struct {
	ExternalID string
	Name       string
	Position   int
}

// SnapshotItem — позиция меню в снапшоте.
type SnapshotItem struct {
	ExternalID         string
	CategoryExternalID string
	Name               string
	Description        string
	PriceMinor         int64
	WeightGrams        *int
	Position           int
	IsAvailable        bool
	OptionGroups       []SnapshotOptionGroup
}

// SnapshotOptionGroup — группа модификаторов позиции в снапшоте.
type SnapshotOptionGroup struct {
	ExternalID string
	Name       string
	MinChoices int
	MaxChoices int
	Position   int
	Options    []SnapshotOption
}

// SnapshotOption — вариант модификатора в снапшоте.
type SnapshotOption struct {
	ExternalID      string
	Name            string
	PriceDeltaMinor int64
	Position        int
	IsAvailable     bool
}

// ImportResult — что сделал импорт.
type ImportResult struct {
	ImportID    uuid.UUID
	Created     int
	Updated     int
	Archived    int
	PayloadHash string
	// Idempotent — снапшот совпал с предыдущим, данные не трогали.
	Idempotent bool
}

// StopTargetKind различает, что именно ставят на стоп: позицию или опцию.
type StopTargetKind string

// Виды целей стоп-листа.
const (
	StopTargetItem   StopTargetKind = "item"
	StopTargetOption StopTargetKind = "option"
)

// StopTarget — цель стоп-листа в идентификаторах платформы.
type StopTarget struct {
	Kind StopTargetKind
	ID   uuid.UUID
}

// AvailabilityChange — переключение доступности одного артикула.
type AvailabilityChange struct {
	ExternalID  string
	IsAvailable bool
	// Until — момент авто-возврата в продажу. Таймер живёт в Temporal,
	// а не в базе: колонки под него нет намеренно (docs/DESIGN.md §6.3).
	Until *time.Time
}

// AvailabilityRequest — батч стоп-листа: позиции и опции в одном запросе.
type AvailabilityRequest struct {
	Items   []AvailabilityChange
	Options []AvailabilityChange
}

// AvailabilityResult — сколько записей переключено и что не нашлось.
type AvailabilityResult struct {
	ItemsUpdated   int
	OptionsUpdated int
	NotFound       []string
}

// VenueSettings — операционные настройки заведения. Указатели отличают
// «не передано» от «передано нулём»: PUT с одним полем не должен обнулять
// остальные.
type VenueSettings struct {
	IsAcceptingOrders   *bool
	MinOrderAmountMinor *int64
	DeliveryFeeMinor    *int64
	DeliveryRadiusM     *int
	AvgPrepMinutes      *int
	Timezone            *string
	// WorkHours заменяет расписание целиком; nil означает «не трогать».
	WorkHours []WorkInterval
}

// WorkInterval — интервал работы в расписании заведения.
type WorkInterval struct {
	Weekday  int
	OpensAt  string
	ClosesAt string
}

// Webhook — куда платформа шлёт заказы и чем их подписывает.
type Webhook struct {
	URL      string
	Secret   string
	IsActive bool
}
