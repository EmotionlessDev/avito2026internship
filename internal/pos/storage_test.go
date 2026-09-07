package pos

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Тесты хранилища гоняются на настоящем SQLite во временном файле: драйвер
// на чистом Go, поэтому это быстро и не требует ни докера, ни моков.
func newTestStorage(t *testing.T) *Storage {
	t.Helper()

	storage, err := OpenStorage(context.Background(), filepath.Join(t.TempDir(), "pos.db"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = storage.Close() })

	return storage
}

func TestStorageSaveOrderIsIdempotent(t *testing.T) {
	t.Parallel()

	storage := newTestStorage(t)
	ctx := context.Background()
	now := time.Now().UTC()

	order := Order{
		ID: "01890000-0100-7000-8000-000000000001", Number: "К-260907-0042",
		Status: statusNew, Payload: `{"items":[]}`, ReceivedAt: now, ActAfter: now,
	}

	saved, err := storage.SaveOrder(ctx, order)
	require.NoError(t, err)
	assert.True(t, saved, "первый заказ записан")

	saved, err = storage.SaveOrder(ctx, order)
	require.NoError(t, err)
	assert.False(t, saved, "повторная доставка того же заказа ничего не меняет")

	orders, err := storage.ListOrders(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, orders, 1)
}

func TestStorageEventDeduplication(t *testing.T) {
	t.Parallel()

	storage := newTestStorage(t)
	ctx := context.Background()

	fresh, err := storage.MarkEventProcessed(ctx, "event-1")
	require.NoError(t, err)
	assert.True(t, fresh)

	fresh, err = storage.MarkEventProcessed(ctx, "event-1")
	require.NoError(t, err)
	assert.False(t, fresh, "повторная доставка события распознана")
}

func TestStorageOrdersDue(t *testing.T) {
	t.Parallel()

	storage := newTestStorage(t)
	ctx := context.Background()
	now := time.Now().UTC()

	ready := Order{
		ID: "ready", Number: "К-1", Status: statusNew, Payload: "{}",
		ReceivedAt: now, ActAfter: now.Add(-time.Second),
	}
	later := Order{
		ID: "later", Number: "К-2", Status: statusNew, Payload: "{}",
		ReceivedAt: now, ActAfter: now.Add(time.Hour),
	}

	for _, order := range []Order{ready, later} {
		_, err := storage.SaveOrder(ctx, order)
		require.NoError(t, err)
	}

	due, err := storage.OrdersDue(ctx, statusNew, now)
	require.NoError(t, err)
	require.Len(t, due, 1, "заказ, до которого не дошло время, кухню не беспокоит")
	assert.Equal(t, "ready", due[0].ID)
}

// Меню переживает перезапуск: повторный сид не затирает цену и стоп-лист,
// выставленные заведением, иначе рестарт сервиса возвращал бы снятое блюдо
// в продажу.
func TestStorageMenuKeepsPriceAndAvailability(t *testing.T) {
	t.Parallel()

	storage := newTestStorage(t)
	ctx := context.Background()

	seed := []MenuItem{{
		ExternalID: "hv-001", Category: "cat", Name: "Хачапури",
		PriceMinor: 52000, Position: 1, IsAvailable: true,
	}}

	require.NoError(t, storage.UpsertMenu(ctx, seed))
	require.NoError(t, storage.SetItemPrice(ctx, "hv-001", 59000))
	require.NoError(t, storage.SetItemAvailability(ctx, "hv-001", false))

	// Повторный сид — как при перезапуске сервиса.
	require.NoError(t, storage.UpsertMenu(ctx, seed))

	items, err := storage.Menu(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)

	assert.Equal(t, int64(59000), items[0].PriceMinor)
	assert.False(t, items[0].IsAvailable)
}

func TestStorageOrderByIDNotFound(t *testing.T) {
	t.Parallel()

	_, err := newTestStorage(t).OrderByID(context.Background(), "нет-такого")
	require.ErrorIs(t, err, ErrOrderNotFound)
}
