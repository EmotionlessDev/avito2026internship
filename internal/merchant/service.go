package merchant

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Store — то, что сервису нужно от хранилища. Интерфейс объявлен
// у потребителя: логика импорта и стоп-листа проверяется без Postgres,
// а репозиторий про сервис ничего не знает.
type Store interface {
	AuthenticateKey(ctx context.Context, keyHash string) (uuid.UUID, error)
	CurrentMenu(ctx context.Context) (CurrentMenu, error)
	LastImportHash(ctx context.Context) (string, error)
	ApplyImport(ctx context.Context, importID uuid.UUID, payloadHash string, changes Changes) error
	SetAvailability(ctx context.Context, request AvailabilityRequest) (AvailabilityResult, []ScheduledReturn, error)
	UpdateVenue(ctx context.Context, settings VenueSettings) error
	VenueSettingsSnapshot(ctx context.Context) (VenueSettings, error)
	UpsertWebhook(ctx context.Context, webhook Webhook) (Webhook, error)
}

// Service — операции контура заведений.
type Service struct {
	store     Store
	scheduler StopListScheduler
	newID     IDGenerator
}

// Option настраивает сервис при сборке.
type Option func(*Service)

// WithIDGenerator подменяет генератор идентификаторов. Нужен тестам:
// сравнивать планы записи со случайными uuid невозможно.
func WithIDGenerator(generator IDGenerator) Option {
	return func(s *Service) { s.newID = generator }
}

// NewService собирает сервис контура заведений.
func NewService(store Store, scheduler StopListScheduler, opts ...Option) *Service {
	service := &Service{store: store, scheduler: scheduler, newID: NewID}

	for _, opt := range opts {
		opt(service)
	}

	return service
}

// Authenticate проверяет API-ключ и возвращает заведение, которому он выдан.
func (s *Service) Authenticate(ctx context.Context, key string) (uuid.UUID, error) {
	return s.store.AuthenticateKey(ctx, HashAPIKey(key))
}

// ImportMenu сводит присланный снапшот с текущим меню.
//
// Идемпотентность проверяется до чтения меню: если отпечаток совпал
// с прошлым импортом, работа не нужна вовсе. Это ровно сценарий M2 —
// заведение шлёт меню по расписанию, а меняется оно редко.
func (s *Service) ImportMenu(ctx context.Context, snapshot Snapshot) (ImportResult, error) {
	payloadHash, err := PayloadHash(snapshot)
	if err != nil {
		return ImportResult{}, err
	}

	lastHash, err := s.store.LastImportHash(ctx)
	if err != nil {
		return ImportResult{}, err
	}

	importID := s.newID()

	if lastHash == payloadHash {
		// Важное следствие: стоп-лист, выставленный отдельной ручкой
		// после импорта, повторный импорт того же снапшота не отменит —
		// отпечаток не изменился, работы нет. Это осознанно: операционное
		// состояние живёт своей жизнью, а разошедшиеся данные ловит
		// суточная сверка меню (docs/DESIGN.md §6.4).
		//
		// Аудит пишем и для повтора: «заведение прислало то же самое»
		// — тоже факт синхронизации, и по нему видно, что интеграция жива.
		if applyErr := s.store.ApplyImport(ctx, importID, payloadHash, Changes{}); applyErr != nil {
			return ImportResult{}, applyErr
		}

		return ImportResult{ImportID: importID, PayloadHash: payloadHash, Idempotent: true}, nil
	}

	current, err := s.store.CurrentMenu(ctx)
	if err != nil {
		return ImportResult{}, err
	}

	changes, err := Diff(current, snapshot, s.newID)
	if err != nil {
		return ImportResult{}, err
	}

	if err := s.store.ApplyImport(ctx, importID, payloadHash, changes); err != nil {
		return ImportResult{}, err
	}

	return ImportResult{
		ImportID:    importID,
		Created:     changes.Created,
		Updated:     changes.Updated,
		Archived:    changes.Archived,
		PayloadHash: payloadHash,
		// Идемпотентным считаем только совпадение отпечатков: пустой diff
		// при другом отпечатке означает, что снапшот изменился незначимо
		// для нас, но это всё-таки новый снапшот.
		Idempotent: false,
	}, nil
}

// SetAvailability переключает стоп-лист позиций и опций.
//
// Авто-возврат проверяется до записи: отказать нужно раньше, чем блюдо
// уедет в стоп-лист. Иначе заведение получило бы 400 уже после того,
// как позиция пропала из продажи.
func (s *Service) SetAvailability(
	ctx context.Context, request AvailabilityRequest,
) (AvailabilityResult, error) {
	if len(request.Items) == 0 && len(request.Options) == 0 {
		return AvailabilityResult{}, ErrEmptyAvailability
	}

	if requestsScheduledReturn(request) && !s.scheduler.Available() {
		return AvailabilityResult{}, ErrScheduledReturnUnavailable
	}

	result, scheduled, err := s.store.SetAvailability(ctx, request)
	if err != nil {
		return AvailabilityResult{}, err
	}

	for _, target := range scheduled {
		if err := s.scheduler.ScheduleReturn(ctx, target.Target, target.Until); err != nil {
			return AvailabilityResult{}, fmt.Errorf("планирование возврата в продажу: %w", err)
		}
	}

	return result, nil
}

// UpdateVenue применяет настройки заведения и возвращает то, что получилось.
func (s *Service) UpdateVenue(ctx context.Context, settings VenueSettings) (VenueSettings, error) {
	if err := s.store.UpdateVenue(ctx, settings); err != nil {
		return VenueSettings{}, err
	}

	return s.store.VenueSettingsSnapshot(ctx)
}

// VenueSettings отдаёт текущие настройки заведения.
func (s *Service) VenueSettings(ctx context.Context) (VenueSettings, error) {
	return s.store.VenueSettingsSnapshot(ctx)
}

// RegisterWebhook сохраняет адрес и секрет подписи.
func (s *Service) RegisterWebhook(ctx context.Context, webhook Webhook) (Webhook, error) {
	return s.store.UpsertWebhook(ctx, webhook)
}

func requestsScheduledReturn(request AvailabilityRequest) bool {
	for _, change := range request.Items {
		if change.Until != nil && !change.IsAvailable {
			return true
		}
	}

	for _, change := range request.Options {
		if change.Until != nil && !change.IsAvailable {
			return true
		}
	}

	return false
}
