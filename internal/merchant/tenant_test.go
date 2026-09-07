package merchant_test

import (
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/merchant"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Изоляция арендаторов держится на этом: репозиторий без заведения
// в контексте обязан отказать, а не выполнить запрос без фильтра.
func TestTenantFrom(t *testing.T) {
	t.Parallel()

	t.Run("заведение проброшено", func(t *testing.T) {
		t.Parallel()

		venueID := uuid.MustParse("01890000-0001-7000-8000-000000000001")

		got, err := merchant.TenantFrom(merchant.WithTenant(testContext(t), venueID))
		require.NoError(t, err)
		assert.Equal(t, venueID, got)
	})

	t.Run("контекст без заведения — ошибка, а не пустой фильтр", func(t *testing.T) {
		t.Parallel()

		_, err := merchant.TenantFrom(testContext(t))
		require.ErrorIs(t, err, merchant.ErrNoTenant)
	})
}

func TestHashAPIKey(t *testing.T) {
	t.Parallel()

	// Значение из сида: демо-ключ «Хачапури и вино».
	assert.Equal(t,
		"e5cd8882e8cd8e0072250e408c5c0c3b1b6fce229690913f4a2679cd698d0b24",
		merchant.HashAPIKey("kitchen_demo_hachapuri_key"),
		"формат хэша обязан совпадать с тем, что лежит в базе после сида")

	assert.NotEqual(t,
		merchant.HashAPIKey("kitchen_demo_hachapuri_key"),
		merchant.HashAPIKey("kitchen_demo_supra_key"))
}

func TestBearerToken(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		header string
		want   string
		ok     bool
	}{
		"обычный заголовок":      {header: "Bearer kitchen_demo_key", want: "kitchen_demo_key", ok: true},
		"регистр схемы не важен": {header: "bearer kitchen_demo_key", want: "kitchen_demo_key", ok: true},
		"лишние пробелы":         {header: "Bearer   kitchen_demo_key  ", want: "kitchen_demo_key", ok: true},
		"пустой заголовок":       {header: "", ok: false},
		"без схемы":              {header: "kitchen_demo_key", ok: false},
		"чужая схема":            {header: "Basic kitchen_demo_key", ok: false},
		"схема без значения":     {header: "Bearer ", ok: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := merchant.BearerToken(tt.header)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAPIKeyPrefix(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "kitchen_demo", merchant.APIKeyPrefix("kitchen_demo_hachapuri_key"))
	assert.Equal(t, "короткий", merchant.APIKeyPrefix("короткий"))
}
