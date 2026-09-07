package publicapi

import (
	"encoding/json"
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/cart"
	"github.com/emotionlessdev/avito-kitchen/internal/gen/public"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ответ валидации обязан совпадать с примером в контракте — это прямой пункт
// DoD этапа 5. Пример берётся из встроенной копии спеки, поэтому тест ломается
// и когда разъедется код, и когда кто-то поправит пример, не поправив маппинг.
func TestValidationResponseMatchesSpecExample(t *testing.T) {
	t.Parallel()

	spec, err := public.GetSwagger()
	require.NoError(t, err)

	schema, ok := spec.Components.Schemas["CartValidation"]
	require.True(t, ok, "в спеке нет схемы CartValidation")
	require.NotNil(t, schema.Value.Example, "у схемы CartValidation пропал пример")

	expected, err := json.Marshal(schema.Value.Example)
	require.NoError(t, err)

	venueID := uuid.MustParse("01890000-0001-7000-8000-000000000001")
	hinkali := uuid.MustParse("01890000-0003-7000-8000-000000000104")
	hachapuri := uuid.MustParse("01890000-0003-7000-8000-000000000101")

	oldPrice, newPrice := int64(45000), int64(52000)
	minOrder, missing := int64(100000), int64(22000)
	deliveryFee := int64(14900)

	result := cart.Result{
		IsValid:          false,
		VenueID:          &venueID,
		ItemsAmountMinor: 78000,
		DeliveryFeeMinor: deliveryFee,
		TotalAmountMinor: 92900,
		Issues: []cart.Issue{
			{
				Code:     cart.CodeItemUnavailable,
				Severity: cart.SeverityBlocking,
				ItemID:   &hinkali,
				ItemName: "Хинкали с бараниной",
				Message:  "Закончились",
			},
			{
				Code:          cart.CodePriceChanged,
				Severity:      cart.SeverityConfirmable,
				ItemID:        &hachapuri,
				OldPriceMinor: &oldPrice,
				NewPriceMinor: &newPrice,
			},
			{
				Code:          cart.CodeMinOrderNotMet,
				Severity:      cart.SeverityBlocking,
				MinOrderMinor: &minOrder,
				MissingMinor:  &missing,
			},
		},
	}

	actual, err := json.Marshal(validationResponse(result))
	require.NoError(t, err)

	assert.JSONEq(t, string(expected), string(actual))
}
