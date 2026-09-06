package merchant_test

import (
	"strconv"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/merchant"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sequentialIDs — предсказуемый генератор: план записи в тестах должен
// сравниваться целиком, а со случайными uuid это невозможно.
func sequentialIDs() merchant.IDGenerator {
	var n int

	return func() uuid.UUID {
		n++

		return uuid.MustParse("01890000-0aaa-7000-8000-" + leftPad(strconv.Itoa(n)))
	}
}

func leftPad(s string) string {
	for len(s) < 12 {
		s = "0" + s
	}

	return s
}

func itemID(n int) uuid.UUID {
	return uuid.MustParse("01890000-0003-7000-8000-" + leftPad(strconv.Itoa(n)))
}

func categoryID(n int) uuid.UUID {
	return uuid.MustParse("01890000-0002-7000-8000-" + leftPad(strconv.Itoa(n)))
}

func snapshotItem(externalID, name string, price int64) merchant.SnapshotItem {
	return merchant.SnapshotItem{
		ExternalID:         externalID,
		CategoryExternalID: "cat-hachapuri",
		Name:               name,
		PriceMinor:         price,
		IsAvailable:        true,
	}
}

func currentItem(id uuid.UUID, name string, price int64) merchant.CurrentItem {
	return merchant.CurrentItem{
		ID:                 id,
		CategoryExternalID: "cat-hachapuri",
		Name:               name,
		PriceMinor:         price,
		IsAvailable:        true,
		OptionGroups:       map[string]merchant.CurrentOptionGroup{},
	}
}

func baseSnapshot(items ...merchant.SnapshotItem) merchant.Snapshot {
	return merchant.Snapshot{
		Categories: []merchant.SnapshotCategory{
			{ExternalID: "cat-hachapuri", Name: "Хачапури", Position: 1},
		},
		Items: items,
	}
}

func baseCurrent(items map[string]merchant.CurrentItem) merchant.CurrentMenu {
	return merchant.CurrentMenu{
		Categories: map[string]merchant.CurrentCategory{
			"cat-hachapuri": {ID: categoryID(1), Name: "Хачапури", Position: 1},
		},
		Items: items,
	}
}

func TestDiffCreatesEverythingOnEmptyMenu(t *testing.T) {
	t.Parallel()

	snapshot := baseSnapshot(
		snapshotItem("hv-001", "Хачапури по-аджарски", 52000),
		snapshotItem("hv-002", "Хачапури по-имеретински", 45000),
	)

	changes, err := merchant.Diff(merchant.CurrentMenu{}, snapshot, sequentialIDs())
	require.NoError(t, err)

	assert.Equal(t, 2, changes.Created)
	assert.Zero(t, changes.Updated)
	assert.Zero(t, changes.Archived)
	assert.Len(t, changes.Categories, 1, "категория тоже создаётся")
	require.Len(t, changes.Items, 2)
	assert.True(t, changes.Items[0].IsNew)
}

// Ключевое свойство импорта: повтор того же снапшота ничего не меняет.
// На нём держится DoD этапа и сценарий M2.
func TestDiffIsIdempotent(t *testing.T) {
	t.Parallel()

	snapshot := baseSnapshot(snapshotItem("hv-001", "Хачапури по-аджарски", 52000))
	current := baseCurrent(map[string]merchant.CurrentItem{
		"hv-001": currentItem(itemID(1), "Хачапури по-аджарски", 52000),
	})

	changes, err := merchant.Diff(current, snapshot, sequentialIDs())
	require.NoError(t, err)

	assert.Zero(t, changes.Created)
	assert.Zero(t, changes.Updated)
	assert.Zero(t, changes.Archived)
	assert.True(t, changes.IsEmpty(), "писать нечего")
}

func TestDiffDetectsFieldChanges(t *testing.T) {
	t.Parallel()

	weight := 450

	tests := map[string]struct {
		snapshot merchant.SnapshotItem
		current  merchant.CurrentItem
	}{
		"изменилась цена": {
			snapshot: snapshotItem("hv-001", "Хачапури по-аджарски", 56000),
			current:  currentItem(itemID(1), "Хачапури по-аджарски", 52000),
		},
		"изменилось название": {
			snapshot: snapshotItem("hv-001", "Хачапури по-аджарски большой", 52000),
			current:  currentItem(itemID(1), "Хачапури по-аджарски", 52000),
		},
		"позиция ушла в стоп-лист снапшотом": {
			snapshot: merchant.SnapshotItem{
				ExternalID: "hv-001", CategoryExternalID: "cat-hachapuri",
				Name: "Хачапури по-аджарски", PriceMinor: 52000, IsAvailable: false,
			},
			current: currentItem(itemID(1), "Хачапури по-аджарски", 52000),
		},
		"появился вес": {
			snapshot: merchant.SnapshotItem{
				ExternalID: "hv-001", CategoryExternalID: "cat-hachapuri",
				Name: "Хачапури по-аджарски", PriceMinor: 52000,
				IsAvailable: true, WeightGrams: &weight,
			},
			current: currentItem(itemID(1), "Хачапури по-аджарски", 52000),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			current := baseCurrent(map[string]merchant.CurrentItem{"hv-001": tt.current})

			changes, err := merchant.Diff(current, baseSnapshot(tt.snapshot), sequentialIDs())
			require.NoError(t, err)

			assert.Equal(t, 1, changes.Updated)
			assert.Zero(t, changes.Created)
			require.Len(t, changes.Items, 1)
			assert.Equal(t, itemID(1), changes.Items[0].ID, "идентификатор позиции сохраняется")
			assert.False(t, changes.Items[0].IsNew)
		})
	}
}

func TestDiffArchivesDisappearedItems(t *testing.T) {
	t.Parallel()

	current := baseCurrent(map[string]merchant.CurrentItem{
		"hv-001": currentItem(itemID(1), "Хачапури по-аджарски", 52000),
		"hv-002": currentItem(itemID(2), "Хачапури по-имеретински", 45000),
	})

	snapshot := baseSnapshot(snapshotItem("hv-001", "Хачапури по-аджарски", 52000))

	changes, err := merchant.Diff(current, snapshot, sequentialIDs())
	require.NoError(t, err)

	assert.Equal(t, 1, changes.Archived)
	assert.Equal(t, []uuid.UUID{itemID(2)}, changes.ArchiveIDs)
	assert.Zero(t, changes.Created, "исчезнувшая позиция не пересоздаётся")
}

func TestDiffDoesNotArchiveTwice(t *testing.T) {
	t.Parallel()

	archived := currentItem(itemID(2), "Хачапури по-имеретински", 45000)
	archived.IsArchived = true

	current := baseCurrent(map[string]merchant.CurrentItem{"hv-002": archived})

	changes, err := merchant.Diff(current, baseSnapshot(), sequentialIDs())
	require.NoError(t, err)

	assert.Zero(t, changes.Archived, "позиция уже архивирована — второй раз не считается")
	assert.Empty(t, changes.ArchiveIDs)
}

// Заведение вернуло в меню то, что раньше пропало. Это та же позиция
// с тем же артикулом: она обновляется, а не создаётся заново, иначе
// исторические заказы потеряли бы связь с ней.
func TestDiffRestoresArchivedItem(t *testing.T) {
	t.Parallel()

	archived := currentItem(itemID(1), "Хачапури по-аджарски", 52000)
	archived.IsArchived = true

	current := baseCurrent(map[string]merchant.CurrentItem{"hv-001": archived})
	snapshot := baseSnapshot(snapshotItem("hv-001", "Хачапури по-аджарски", 52000))

	changes, err := merchant.Diff(current, snapshot, sequentialIDs())
	require.NoError(t, err)

	assert.Equal(t, 1, changes.Updated)
	assert.Zero(t, changes.Created)
	require.Len(t, changes.Items, 1)
	assert.Equal(t, itemID(1), changes.Items[0].ID)
}

func TestDiffOptionGroups(t *testing.T) {
	t.Parallel()

	withGroup := snapshotItem("hv-001", "Хачапури по-аджарски", 52000)
	withGroup.OptionGroups = []merchant.SnapshotOptionGroup{{
		ExternalID: "hv-001-extra", Name: "Добавки", MaxChoices: 3,
		Options: []merchant.SnapshotOption{
			{ExternalID: "cheese", Name: "Двойной сыр", PriceDeltaMinor: 9000, IsAvailable: true},
		},
	}}

	t.Run("новая группа делает позицию изменённой", func(t *testing.T) {
		t.Parallel()

		current := baseCurrent(map[string]merchant.CurrentItem{
			"hv-001": currentItem(itemID(1), "Хачапури по-аджарски", 52000),
		})

		changes, err := merchant.Diff(current, baseSnapshot(withGroup), sequentialIDs())
		require.NoError(t, err)

		assert.Equal(t, 1, changes.Updated)
		require.Len(t, changes.Items[0].Groups, 1)
		require.Len(t, changes.Items[0].Groups[0].Options, 1)
	})

	t.Run("исчезнувшая опция попадает в удаление", func(t *testing.T) {
		t.Parallel()

		item := currentItem(itemID(1), "Хачапури по-аджарски", 52000)
		item.OptionGroups = map[string]merchant.CurrentOptionGroup{
			"hv-001-extra": {
				ID:   uuid.MustParse("01890000-0004-7000-8000-000000000001"),
				Name: "Добавки", MaxChoices: 3,
				Options: map[string]merchant.CurrentOption{
					"cheese": {
						ID:              uuid.MustParse("01890000-0005-7000-8000-000000000001"),
						Name:            "Двойной сыр",
						PriceDeltaMinor: 9000,
						IsAvailable:     true,
					},
					"egg": {
						ID:              uuid.MustParse("01890000-0005-7000-8000-000000000002"),
						Name:            "Яйцо",
						PriceDeltaMinor: 5000,
						IsAvailable:     true,
					},
				},
			},
		}

		current := baseCurrent(map[string]merchant.CurrentItem{"hv-001": item})

		changes, err := merchant.Diff(current, baseSnapshot(withGroup), sequentialIDs())
		require.NoError(t, err)

		assert.Equal(t, 1, changes.Updated)
		require.Len(t, changes.Items, 1)
		assert.Equal(t,
			[]uuid.UUID{uuid.MustParse("01890000-0005-7000-8000-000000000002")},
			changes.Items[0].Groups[0].RemoveOptionIDs)
	})

	t.Run("совпавшие группы не считаются изменением", func(t *testing.T) {
		t.Parallel()

		item := currentItem(itemID(1), "Хачапури по-аджарски", 52000)
		item.OptionGroups = map[string]merchant.CurrentOptionGroup{
			"hv-001-extra": {
				ID:   uuid.MustParse("01890000-0004-7000-8000-000000000001"),
				Name: "Добавки", MaxChoices: 3,
				Options: map[string]merchant.CurrentOption{
					"cheese": {
						ID:              uuid.MustParse("01890000-0005-7000-8000-000000000001"),
						Name:            "Двойной сыр",
						PriceDeltaMinor: 9000,
						IsAvailable:     true,
					},
				},
			},
		}

		current := baseCurrent(map[string]merchant.CurrentItem{"hv-001": item})

		changes, err := merchant.Diff(current, baseSnapshot(withGroup), sequentialIDs())
		require.NoError(t, err)

		assert.True(t, changes.IsEmpty(), "ничего не поменялось — писать нечего")
	})
}

func TestDiffRejectsBrokenSnapshot(t *testing.T) {
	t.Parallel()

	tests := map[string]merchant.Snapshot{
		"позиция ссылается на несуществующую категорию": {
			Categories: []merchant.SnapshotCategory{{ExternalID: "cat-hachapuri", Name: "Хачапури"}},
			Items: []merchant.SnapshotItem{{
				ExternalID: "hv-001", CategoryExternalID: "cat-unknown",
				Name: "Хачапури", PriceMinor: 52000,
			}},
		},
		"артикул позиции повторяется": {
			Categories: []merchant.SnapshotCategory{{ExternalID: "cat-hachapuri", Name: "Хачапури"}},
			Items: []merchant.SnapshotItem{
				snapshotItem("hv-001", "Первый", 52000),
				snapshotItem("hv-001", "Второй", 45000),
			},
		},
		"артикул категории повторяется": {
			Categories: []merchant.SnapshotCategory{
				{ExternalID: "cat-hachapuri", Name: "Хачапури"},
				{ExternalID: "cat-hachapuri", Name: "Хачапури ещё раз"},
			},
		},
	}

	for name, snapshot := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := merchant.Diff(merchant.CurrentMenu{}, snapshot, sequentialIDs())
			require.ErrorIs(t, err, merchant.ErrInvalidSnapshot)
		})
	}
}

// Хэш нужен идемпотентности: одно и то же меню, присланное в другом порядке,
// обязано дать тот же отпечаток, иначе повторный импорт выглядел бы новым.
func TestPayloadHash(t *testing.T) {
	t.Parallel()

	first := baseSnapshot(
		snapshotItem("hv-001", "Хачапури по-аджарски", 52000),
		snapshotItem("hv-002", "Хачапури по-имеретински", 45000),
	)
	reordered := baseSnapshot(
		snapshotItem("hv-002", "Хачапури по-имеретински", 45000),
		snapshotItem("hv-001", "Хачапури по-аджарски", 52000),
	)
	changed := baseSnapshot(
		snapshotItem("hv-001", "Хачапури по-аджарски", 56000),
		snapshotItem("hv-002", "Хачапури по-имеретински", 45000),
	)

	firstHash, err := merchant.PayloadHash(first)
	require.NoError(t, err)

	reorderedHash, err := merchant.PayloadHash(reordered)
	require.NoError(t, err)

	changedHash, err := merchant.PayloadHash(changed)
	require.NoError(t, err)

	assert.Equal(t, firstHash, reorderedHash, "порядок позиций не меняет смысла меню")
	assert.NotEqual(t, firstHash, changedHash, "изменённая цена обязана менять отпечаток")
	assert.Len(t, firstHash, 64, "sha256 в hex")
}
