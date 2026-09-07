package pos

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
)

// menuFile — меню заведения в его собственном формате.
//
// Это данные учётной системы ресторана, а не платформы: артикулы (hv-001)
// принадлежат заведению, и платформа узнаёт о них только из импорта.
//
//go:embed menu.json
var menuFile []byte

// menuSeed — меню, которое заведение заводит у себя при первом запуске.
type menuSeed struct {
	Items      []MenuItem     `json:"items"`
	Categories []menuCategory `json:"categories"`
	// OptionGroups живут только в этом файле, а не в SQLite заведения:
	// модификаторы у POS не меняются, а таблица под них добавила бы
	// хранилищу сложности без пользы для демо.
	OptionGroups []menuOptionGroup `json:"option_groups"`
}

// menuOptionGroup — группа модификаторов позиции в меню заведения.
type menuOptionGroup struct {
	ItemExternalID string       `json:"item_external_id"`
	ExternalID     string       `json:"external_id"`
	Name           string       `json:"name"`
	MinChoices     int          `json:"min_choices"`
	MaxChoices     int          `json:"max_choices"`
	Position       int          `json:"position"`
	Options        []menuOption `json:"options"`
}

// menuOption — один модификатор.
type menuOption struct {
	ExternalID      string `json:"external_id"`
	Name            string `json:"name"`
	PriceDeltaMinor int64  `json:"price_delta_minor"`
	Position        int    `json:"position"`
}

type menuCategory struct {
	ExternalID string `json:"external_id"`
	Name       string `json:"name"`
	Position   int    `json:"position"`
}

func loadMenuSeed() (menuSeed, error) {
	var seed menuSeed

	if err := json.Unmarshal(menuFile, &seed); err != nil {
		return menuSeed{}, fmt.Errorf("разбор меню заведения: %w", err)
	}

	return seed, nil
}

// seedMenu заполняет локальное меню при первом запуске.
func (s *Service) seedMenu(ctx context.Context) error {
	seed, err := loadMenuSeed()
	if err != nil {
		return err
	}

	s.categories = seed.Categories
	s.optionGroups = seed.OptionGroups

	return s.storage.UpsertMenu(ctx, seed.Items)
}
