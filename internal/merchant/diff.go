package merchant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// CurrentMenu — снимок того, что о меню заведения знает платформа. Ключи —
// артикулы заведения: именно по ним снапшот сводится с нашими данными
// (README.md, раздел «О проекте»).
type CurrentMenu struct {
	Categories map[string]CurrentCategory
	Items      map[string]CurrentItem
}

// CurrentCategory — раздел меню, уже заведённый на платформе.
type CurrentCategory struct {
	ID       uuid.UUID
	Name     string
	Position int
}

// CurrentItem — позиция меню, уже заведённая на платформе.
type CurrentItem struct {
	ID                 uuid.UUID
	CategoryExternalID string
	Name               string
	Description        string
	PriceMinor         int64
	WeightGrams        *int
	Position           int
	IsAvailable        bool
	IsArchived         bool
	OptionGroups       map[string]CurrentOptionGroup
}

// CurrentOptionGroup — группа модификаторов, уже заведённая на платформе.
type CurrentOptionGroup struct {
	ID         uuid.UUID
	Name       string
	MinChoices int
	MaxChoices int
	Position   int
	Options    map[string]CurrentOption
}

// CurrentOption — модификатор, уже заведённый на платформе.
type CurrentOption struct {
	ID              uuid.UUID
	Name            string
	PriceDeltaMinor int64
	Position        int
	IsAvailable     bool
}

// Changes — план записи: что создать, что обновить, что заархивировать.
//
// Отдельный тип между вычислением и записью нужен, чтобы алгоритм diff
// проверялся без базы, а репозиторий не принимал решений.
type Changes struct {
	Categories []CategoryChange
	Items      []ItemChange
	ArchiveIDs []uuid.UUID

	Created  int
	Updated  int
	Archived int
}

// IsEmpty сообщает, что писать нечего: снапшот совпал с текущим состоянием.
func (c Changes) IsEmpty() bool {
	return len(c.Categories) == 0 && len(c.Items) == 0 && len(c.ArchiveIDs) == 0
}

// CategoryChange — раздел меню, который нужно создать или обновить.
type CategoryChange struct {
	ID         uuid.UUID
	ExternalID string
	Name       string
	Position   int
}

// ItemChange — позиция целиком, вместе с модификаторами. Позиция пишется
// полностью, а не по изменённым полям: так запись не зависит от того,
// что именно поменялось, и остаётся идемпотентной.
type ItemChange struct {
	ID          uuid.UUID
	IsNew       bool
	CategoryID  uuid.UUID
	ExternalID  string
	Name        string
	Description string
	PriceMinor  int64
	WeightGrams *int
	Position    int
	IsAvailable bool

	Groups         []GroupChange
	RemoveGroupIDs []uuid.UUID
}

// GroupChange — группа модификаторов, которую нужно создать или обновить.
type GroupChange struct {
	ID              uuid.UUID
	ExternalID      string
	Name            string
	MinChoices      int
	MaxChoices      int
	Position        int
	Options         []OptionChange
	RemoveOptionIDs []uuid.UUID
}

// OptionChange — модификатор, который нужно создать или обновить.
type OptionChange struct {
	ID              uuid.UUID
	ExternalID      string
	Name            string
	PriceDeltaMinor int64
	Position        int
	IsAvailable     bool
}

// IDGenerator выдаёт идентификаторы новым сущностям. Отдельный тип, потому
// что в тестах нужен предсказуемый генератор, а в проде — UUID v7.
type IDGenerator func() uuid.UUID

// NewID — идентификатор для новых записей: UUID v7, k-sortable, генерируется
// приложением (docs/adr/0005-uuid-v7-in-app.md).
func NewID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		// Единственная причина ошибки — отказ источника случайности.
		// Версия 4 здесь честнее паники: теряется локальность в индексе,
		// но не работоспособность импорта.
		return uuid.New()
	}

	return id
}

// Diff сводит присланный снапшот с текущим состоянием меню.
//
// Правила: чего нет на платформе — создаётся, что изменилось — обновляется,
// чего нет в снапшоте — архивируется мягко (на позицию ссылаются исторические
// заказы). Позиция, вернувшаяся в меню после архивации, считается обновлённой,
// а не созданной: это та же позиция с тем же артикулом.
func Diff(current CurrentMenu, snapshot Snapshot, newID IDGenerator) (Changes, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return Changes{}, err
	}

	var changes Changes

	categoryIDs := make(map[string]uuid.UUID, len(snapshot.Categories))

	for _, category := range snapshot.Categories {
		existing, found := current.Categories[category.ExternalID]

		id := existing.ID
		if !found {
			id = newID()
		}

		categoryIDs[category.ExternalID] = id

		if !found || existing.Name != category.Name || existing.Position != category.Position {
			changes.Categories = append(changes.Categories, CategoryChange{
				ID:         id,
				ExternalID: category.ExternalID,
				Name:       category.Name,
				Position:   category.Position,
			})
		}
	}

	for _, item := range snapshot.Items {
		categoryID, ok := categoryIDs[item.CategoryExternalID]
		if !ok {
			return Changes{}, fmt.Errorf("%w: позиция %q ссылается на категорию %q, которой нет в снапшоте",
				ErrInvalidSnapshot, item.ExternalID, item.CategoryExternalID)
		}

		existing, found := current.Items[item.ExternalID]

		change := itemChange(item, existing, found, categoryID, newID)

		switch {
		case !found:
			changes.Created++
			changes.Items = append(changes.Items, change)
		case itemDiffers(existing, item, change):
			changes.Updated++
			changes.Items = append(changes.Items, change)
		}
	}

	inSnapshot := make(map[string]struct{}, len(snapshot.Items))
	for _, item := range snapshot.Items {
		inSnapshot[item.ExternalID] = struct{}{}
	}

	for externalID, item := range current.Items {
		if _, kept := inSnapshot[externalID]; kept || item.IsArchived {
			continue
		}

		changes.ArchiveIDs = append(changes.ArchiveIDs, item.ID)
		changes.Archived++
	}

	// Порядок записи не должен зависеть от обхода map: одинаковый снапшот
	// обязан давать одинаковый план, иначе тесты ловят призраков.
	slices.SortFunc(changes.ArchiveIDs, compareIDs)

	return changes, nil
}

func itemChange(
	item SnapshotItem, existing CurrentItem, found bool, categoryID uuid.UUID, newID IDGenerator,
) ItemChange {
	id := existing.ID
	if !found {
		id = newID()
	}

	change := ItemChange{
		ID:          id,
		IsNew:       !found,
		CategoryID:  categoryID,
		ExternalID:  item.ExternalID,
		Name:        item.Name,
		Description: item.Description,
		PriceMinor:  item.PriceMinor,
		WeightGrams: item.WeightGrams,
		Position:    item.Position,
		IsAvailable: item.IsAvailable,
	}

	seenGroups := make(map[string]struct{}, len(item.OptionGroups))

	for _, group := range item.OptionGroups {
		seenGroups[group.ExternalID] = struct{}{}
		change.Groups = append(change.Groups, groupChange(group, existing.OptionGroups[group.ExternalID], newID))
	}

	for externalID, group := range existing.OptionGroups {
		if _, kept := seenGroups[externalID]; !kept {
			change.RemoveGroupIDs = append(change.RemoveGroupIDs, group.ID)
		}
	}

	slices.SortFunc(change.RemoveGroupIDs, compareIDs)

	return change
}

func groupChange(group SnapshotOptionGroup, existing CurrentOptionGroup, newID IDGenerator) GroupChange {
	id := existing.ID
	if id == uuid.Nil {
		id = newID()
	}

	change := GroupChange{
		ID:         id,
		ExternalID: group.ExternalID,
		Name:       group.Name,
		MinChoices: group.MinChoices,
		MaxChoices: group.MaxChoices,
		Position:   group.Position,
	}

	seenOptions := make(map[string]struct{}, len(group.Options))

	for _, option := range group.Options {
		seenOptions[option.ExternalID] = struct{}{}

		optionID := existing.Options[option.ExternalID].ID
		if optionID == uuid.Nil {
			optionID = newID()
		}

		change.Options = append(change.Options, OptionChange{
			ID:              optionID,
			ExternalID:      option.ExternalID,
			Name:            option.Name,
			PriceDeltaMinor: option.PriceDeltaMinor,
			Position:        option.Position,
			IsAvailable:     option.IsAvailable,
		})
	}

	for externalID, option := range existing.Options {
		if _, kept := seenOptions[externalID]; !kept {
			change.RemoveOptionIDs = append(change.RemoveOptionIDs, option.ID)
		}
	}

	slices.SortFunc(change.RemoveOptionIDs, compareIDs)

	return change
}

// itemDiffers отвечает, изменилось ли хоть что-то в позиции. Архивированная
// позиция, вернувшаяся в снапшот, считается изменившейся: её нужно вернуть
// в продажу.
func itemDiffers(existing CurrentItem, item SnapshotItem, change ItemChange) bool {
	if existing.IsArchived {
		return true
	}

	if existing.Name != item.Name ||
		existing.Description != item.Description ||
		existing.PriceMinor != item.PriceMinor ||
		existing.Position != item.Position ||
		existing.IsAvailable != item.IsAvailable ||
		existing.CategoryExternalID != item.CategoryExternalID ||
		!equalWeight(existing.WeightGrams, item.WeightGrams) {
		return true
	}

	return groupsDiffer(existing.OptionGroups, change)
}

func groupsDiffer(existing map[string]CurrentOptionGroup, change ItemChange) bool {
	if len(change.RemoveGroupIDs) > 0 || len(existing) != len(change.Groups) {
		return true
	}

	for i := range change.Groups {
		if groupDiffers(existing[change.Groups[i].ExternalID], change.Groups[i]) {
			return true
		}
	}

	return false
}

func groupDiffers(current CurrentOptionGroup, group GroupChange) bool {
	if current.ID == uuid.Nil ||
		current.Name != group.Name ||
		current.MinChoices != group.MinChoices ||
		current.MaxChoices != group.MaxChoices ||
		current.Position != group.Position ||
		len(current.Options) != len(group.Options) ||
		len(group.RemoveOptionIDs) > 0 {
		return true
	}

	for i := range group.Options {
		if optionDiffers(current.Options[group.Options[i].ExternalID], group.Options[i]) {
			return true
		}
	}

	return false
}

func optionDiffers(current CurrentOption, option OptionChange) bool {
	return current.ID == uuid.Nil ||
		current.Name != option.Name ||
		current.PriceDeltaMinor != option.PriceDeltaMinor ||
		current.Position != option.Position ||
		current.IsAvailable != option.IsAvailable
}

func equalWeight(a, b *int) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

func validateSnapshot(snapshot Snapshot) error {
	categories := make(map[string]struct{}, len(snapshot.Categories))

	for _, category := range snapshot.Categories {
		if _, duplicate := categories[category.ExternalID]; duplicate {
			return fmt.Errorf("%w: категория %q встречается дважды", ErrInvalidSnapshot, category.ExternalID)
		}

		categories[category.ExternalID] = struct{}{}
	}

	items := make(map[string]struct{}, len(snapshot.Items))

	for _, item := range snapshot.Items {
		if _, duplicate := items[item.ExternalID]; duplicate {
			return fmt.Errorf("%w: позиция %q встречается дважды", ErrInvalidSnapshot, item.ExternalID)
		}

		items[item.ExternalID] = struct{}{}

		if err := validateGroups(item); err != nil {
			return err
		}
	}

	return nil
}

func validateGroups(item SnapshotItem) error {
	groups := make(map[string]struct{}, len(item.OptionGroups))

	for _, group := range item.OptionGroups {
		if _, duplicate := groups[group.ExternalID]; duplicate {
			return fmt.Errorf("%w: группа %q позиции %q встречается дважды",
				ErrInvalidSnapshot, group.ExternalID, item.ExternalID)
		}

		groups[group.ExternalID] = struct{}{}

		options := make(map[string]struct{}, len(group.Options))

		for _, option := range group.Options {
			if _, duplicate := options[option.ExternalID]; duplicate {
				return fmt.Errorf("%w: опция %q группы %q встречается дважды",
					ErrInvalidSnapshot, option.ExternalID, group.ExternalID)
			}

			options[option.ExternalID] = struct{}{}
		}
	}

	return nil
}

// PayloadHash считает отпечаток снапшота для идемпотентности.
//
// Перед хэшированием списки сортируются по артикулам: заведение имеет право
// прислать то же меню в другом порядке, и это не повод считать импорт новым.
func PayloadHash(snapshot Snapshot) (string, error) {
	canonical := canonicalSnapshot(snapshot)

	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("хэш снапшота: %w", err)
	}

	sum := sha256.Sum256(encoded)

	return hex.EncodeToString(sum[:]), nil
}

func canonicalSnapshot(snapshot Snapshot) Snapshot {
	categories := slices.Clone(snapshot.Categories)
	slices.SortFunc(categories, func(a, b SnapshotCategory) int {
		return compareStrings(a.ExternalID, b.ExternalID)
	})

	items := slices.Clone(snapshot.Items)
	slices.SortFunc(items, func(a, b SnapshotItem) int {
		return compareStrings(a.ExternalID, b.ExternalID)
	})

	for i := range items {
		groups := slices.Clone(items[i].OptionGroups)
		slices.SortFunc(groups, func(a, b SnapshotOptionGroup) int {
			return compareStrings(a.ExternalID, b.ExternalID)
		})

		for j := range groups {
			options := slices.Clone(groups[j].Options)
			slices.SortFunc(options, func(a, b SnapshotOption) int {
				return compareStrings(a.ExternalID, b.ExternalID)
			})

			groups[j].Options = options
		}

		items[i].OptionGroups = groups
	}

	return Snapshot{Categories: categories, Items: items}
}

// compareIDs упорядочивает идентификаторы: план записи не должен зависеть
// от порядка обхода map, иначе одинаковый снапшот даёт разные планы.
func compareIDs(a, b uuid.UUID) int {
	return compareStrings(a.String(), b.String())
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
