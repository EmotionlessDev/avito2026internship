package gen_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/gen/merchant"
	"github.com/emotionlessdev/avito-kitchen/internal/gen/pos"
	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Тест ловит забытый `make generate`: сгенерированный код несёт в себе копию
// спеки, и если YAML успели поправить, а код не пересобрали, наборы операций
// разойдутся. Без такой проверки расхождение всплывает уже в рантайме —
// хендлер есть в интерфейсе, а роутинга под него нет.
func TestGeneratedCodeMatchesSpecs(t *testing.T) {
	t.Parallel()

	specs := map[string]struct {
		file      string
		embedded  func() (*openapi3.T, error)
		operation string
	}{
		"public":       {"public.openapi.yaml", public.GetSwagger, "validatecart"},
		"merchant":     {"merchant.openapi.yaml", merchant.GetSwagger, "importmenu"},
		"pos-webhooks": {"pos-webhooks.openapi.yaml", pos.GetSwagger, "deliverorder"},
	}

	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			embedded, err := spec.embedded()
			require.NoError(t, err, "встроенная в код спека не разбирается")

			loader := openapi3.NewLoader()
			loader.IsExternalRefsAllowed = false

			fromFile, err := loader.LoadFromFile(filepath.Join("..", "..", "api", spec.file))
			require.NoError(t, err, "спека на диске не разбирается")

			// Сравниваем без учёта регистра: во встроенную копию спеки
			// oapi-codegen кладёт операции уже под Go-именами (PascalCase).
			assert.Equal(t, operationIDs(fromFile), operationIDs(embedded),
				"код разошёлся со спекой — нужен `make generate`")
			assert.Contains(t, operationIDs(embedded), spec.operation,
				"ключевая операция контура пропала из спеки")
		})
	}
}

// Статусы заказа — общий словарь трёх контуров и схемы БД. Разъехавшийся
// список означает, что где-то забыли добавить новый статус: миграция
// 0003_orders и оба контура обязаны видеть одинаковый набор.
func TestOrderStatusEnumIsSharedBetweenContours(t *testing.T) {
	t.Parallel()

	publicSpec, err := public.GetSwagger()
	require.NoError(t, err)

	merchantSpec, err := merchant.GetSwagger()
	require.NoError(t, err)

	assert.Equal(t,
		enumValues(t, publicSpec, "OrderStatus"),
		enumValues(t, merchantSpec, "OrderStatus"))
}

func operationIDs(spec *openapi3.T) []string {
	ids := make([]string, 0, spec.Paths.Len())

	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			ids = append(ids, strings.ToLower(op.OperationID))
		}
	}

	sort.Strings(ids)

	return ids
}

func enumValues(t *testing.T, spec *openapi3.T, schema string) []any {
	t.Helper()

	ref, ok := spec.Components.Schemas[schema]
	require.True(t, ok, "в спеке нет схемы %s", schema)

	return ref.Value.Enum
}
