package merchant_test

import (
	"context"
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/merchant"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore — хранилище контура в памяти. Транзакции и SQL проверяются
// интеграционными тестами на настоящей базе (этап 11); здесь проверяются
// решения сервиса: когда работать, когда отказать, что записать.
type fakeStore struct {
	venueID      uuid.UUID
	keyHash      string
	current      merchant.CurrentMenu
	lastHash     string
	applied      []merchant.Changes
	appliedHash  []string
	availability merchant.AvailabilityResult
	scheduled    []merchant.ScheduledReturn
	settings     merchant.VenueSettings
	webhook      merchant.Webhook
	stats        merchant.DailyStats
}

func (f *fakeStore) AuthenticateKey(_ context.Context, keyHash string) (uuid.UUID, error) {
	if keyHash != f.keyHash {
		return uuid.Nil, merchant.ErrUnauthorized
	}

	return f.venueID, nil
}

func (f *fakeStore) CurrentMenu(_ context.Context) (merchant.CurrentMenu, error) {
	return f.current, nil
}

func (f *fakeStore) LastImportHash(_ context.Context) (string, error) {
	return f.lastHash, nil
}

func (f *fakeStore) ApplyImport(
	_ context.Context, _ uuid.UUID, payloadHash string, changes merchant.Changes,
) error {
	f.applied = append(f.applied, changes)
	f.appliedHash = append(f.appliedHash, payloadHash)
	f.lastHash = payloadHash

	return nil
}

func (f *fakeStore) SetAvailability(
	_ context.Context, request merchant.AvailabilityRequest,
) (merchant.AvailabilityResult, []merchant.ScheduledReturn, error) {
	result := merchant.AvailabilityResult{
		ItemsUpdated:   len(request.Items),
		OptionsUpdated: len(request.Options),
	}

	var scheduled []merchant.ScheduledReturn

	for _, change := range request.Items {
		if change.Until != nil && !change.IsAvailable {
			scheduled = append(scheduled, merchant.ScheduledReturn{
				Target: merchant.StopTarget{Kind: merchant.StopTargetItem, ID: uuid.New()},
				Until:  *change.Until,
			})
		}
	}

	f.availability = result
	f.scheduled = scheduled

	return result, scheduled, nil
}

func (f *fakeStore) UpdateVenue(_ context.Context, settings merchant.VenueSettings) error {
	f.settings = settings

	return nil
}

func (f *fakeStore) VenueSettingsSnapshot(_ context.Context) (merchant.VenueSettings, error) {
	return f.settings, nil
}

func (f *fakeStore) UpsertWebhook(_ context.Context, webhook merchant.Webhook) (merchant.Webhook, error) {
	f.webhook = webhook

	return merchant.Webhook{URL: webhook.URL, IsActive: true}, nil
}

func (f *fakeStore) DailyStats(_ context.Context) (merchant.DailyStats, error) {
	return f.stats, nil
}

// recordingScheduler — планировщик, который умеет всё и запоминает вызовы.
// Так проверяется, что сервис доносит до него запрошенные таймеры.
type recordingScheduler struct {
	calls []merchant.StopTarget
}

func (r *recordingScheduler) Available() bool { return true }

func (r *recordingScheduler) ScheduleReturn(
	_ context.Context, target merchant.StopTarget, _ time.Time,
) error {
	r.calls = append(r.calls, target)

	return nil
}

func (r *recordingScheduler) CancelReturn(_ context.Context, _ merchant.StopTarget) error {
	return nil
}

func TestImportMenuAppliesChanges(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	service := merchant.NewService(store, merchant.UnsupportedScheduler{}, merchant.WithIDGenerator(sequentialIDs()))

	snapshot := baseSnapshot(snapshotItem("hv-001", "Хачапури по-аджарски", 52000))

	result, err := service.ImportMenu(testContext(t), snapshot)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Created)
	assert.False(t, result.Idempotent)
	assert.Len(t, result.PayloadHash, 64)
	require.Len(t, store.applied, 1)
	assert.Equal(t, 1, store.applied[0].Created)
}

// DoD этапа: повторный импорт того же снапшота ничего не меняет и говорит
// об этом явно.
func TestImportMenuIsIdempotent(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	service := merchant.NewService(store, merchant.UnsupportedScheduler{}, merchant.WithIDGenerator(sequentialIDs()))

	snapshot := baseSnapshot(snapshotItem("hv-001", "Хачапури по-аджарски", 52000))

	first, err := service.ImportMenu(testContext(t), snapshot)
	require.NoError(t, err)

	second, err := service.ImportMenu(testContext(t), snapshot)
	require.NoError(t, err)

	assert.Equal(t, 1, first.Created)
	assert.True(t, second.Idempotent)
	assert.Zero(t, second.Created)
	assert.Zero(t, second.Updated)
	assert.Equal(t, first.PayloadHash, second.PayloadHash)

	require.Len(t, store.applied, 2, "аудит пишется и для повтора")
	assert.True(t, store.applied[1].IsEmpty(), "но данные не трогаются")
}

// Меню, откатанное к предыдущему снапшоту, обязано примениться: сравнение
// идёт с последним импортом, а не со всей историей.
func TestImportMenuAppliesRollbackToPreviousSnapshot(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	service := merchant.NewService(store, merchant.UnsupportedScheduler{}, merchant.WithIDGenerator(sequentialIDs()))

	versionA := baseSnapshot(snapshotItem("hv-001", "Хачапури по-аджарски", 52000))
	versionB := baseSnapshot(snapshotItem("hv-001", "Хачапури по-аджарски", 56000))

	_, err := service.ImportMenu(testContext(t), versionA)
	require.NoError(t, err)

	_, err = service.ImportMenu(testContext(t), versionB)
	require.NoError(t, err)

	back, err := service.ImportMenu(testContext(t), versionA)
	require.NoError(t, err)

	assert.False(t, back.Idempotent, "возврат к прежнему меню — полноценный импорт")
}

func TestImportMenuRejectsBrokenSnapshot(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	service := merchant.NewService(store, merchant.UnsupportedScheduler{})

	broken := merchant.Snapshot{
		Items: []merchant.SnapshotItem{{
			ExternalID: "hv-001", CategoryExternalID: "cat-unknown",
			Name: "Хачапури", PriceMinor: 52000,
		}},
	}

	_, err := service.ImportMenu(testContext(t), broken)
	require.ErrorIs(t, err, merchant.ErrInvalidSnapshot)
	assert.Empty(t, store.applied, "битый снапшот не доезжает до базы")
}

func TestSetAvailabilityRejectsEmptyRequest(t *testing.T) {
	t.Parallel()

	service := merchant.NewService(&fakeStore{}, merchant.UnsupportedScheduler{})

	_, err := service.SetAvailability(testContext(t), merchant.AvailabilityRequest{})
	require.ErrorIs(t, err, merchant.ErrEmptyAvailability)
}

// Пока планировщика нет, авто-возврат отклоняется до записи: заведение
// не должно узнать об отказе после того, как блюдо уже снято с продажи.
func TestSetAvailabilityRejectsScheduledReturnBeforeWriting(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	service := merchant.NewService(store, merchant.UnsupportedScheduler{})

	until := time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)

	_, err := service.SetAvailability(testContext(t), merchant.AvailabilityRequest{
		Items: []merchant.AvailabilityChange{
			{ExternalID: "hv-004", IsAvailable: false, Until: &until},
		},
	})

	require.ErrorIs(t, err, merchant.ErrScheduledReturnUnavailable)
	assert.Zero(t, store.availability.ItemsUpdated, "в базу ничего не ушло")
}

// Тот же запрос с рабочим планировщиком проходит целиком — на этапе 7
// достаточно подменить реализацию.
func TestSetAvailabilitySchedulesReturnWhenPlannerAvailable(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	scheduler := &recordingScheduler{}
	service := merchant.NewService(store, scheduler)

	until := time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)

	result, err := service.SetAvailability(testContext(t), merchant.AvailabilityRequest{
		Items: []merchant.AvailabilityChange{
			{ExternalID: "hv-004", IsAvailable: false, Until: &until},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.ItemsUpdated)
	require.Len(t, scheduler.calls, 1)
	assert.Equal(t, merchant.StopTargetItem, scheduler.calls[0].Kind)
}

// Обычный стоп-лист без таймера работает и без планировщика.
func TestSetAvailabilityWithoutScheduledReturn(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	service := merchant.NewService(store, merchant.UnsupportedScheduler{})

	result, err := service.SetAvailability(testContext(t), merchant.AvailabilityRequest{
		Items:   []merchant.AvailabilityChange{{ExternalID: "hv-004", IsAvailable: false}},
		Options: []merchant.AvailabilityChange{{ExternalID: "cheese", IsAvailable: false}},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.ItemsUpdated)
	assert.Equal(t, 1, result.OptionsUpdated)
}

func TestAuthenticate(t *testing.T) {
	t.Parallel()

	venueID := uuid.MustParse("01890000-0001-7000-8000-000000000001")
	store := &fakeStore{venueID: venueID, keyHash: merchant.HashAPIKey("kitchen_demo_key")}
	service := merchant.NewService(store, merchant.UnsupportedScheduler{})

	t.Run("верный ключ", func(t *testing.T) {
		t.Parallel()

		got, err := service.Authenticate(testContext(t), "kitchen_demo_key")
		require.NoError(t, err)
		assert.Equal(t, venueID, got)
	})

	t.Run("чужой ключ", func(t *testing.T) {
		t.Parallel()

		_, err := service.Authenticate(testContext(t), "не тот ключ")
		require.ErrorIs(t, err, merchant.ErrUnauthorized)
	})
}
