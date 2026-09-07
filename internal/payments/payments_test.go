package payments_test

import (
	"testing"

	"github.com/emotionlessdev/avito-kitchen/internal/payments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var orderID = uuid.MustParse("01890000-0100-7000-8000-000000000001")

// Отказ обязан быть предсказуемым: на нём держится и демо-сценарий U14,
// и повторяемость replay в Temporal.
func TestMockAuthorizeIsDeterministic(t *testing.T) {
	t.Parallel()

	provider := payments.NewMock()

	tests := map[string]struct {
		amountMinor int64
		wantErr     bool
	}{
		"обычная сумма":             {amountMinor: 118900, wantErr: false},
		"сумма на 99 копеек":        {amountMinor: 118999, wantErr: true},
		"другая сумма на 99 копеек": {amountMinor: 99, wantErr: true},
		"почти несчастливая":        {amountMinor: 118998, wantErr: false},
		"ноль":                      {amountMinor: 0, wantErr: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ref, err := provider.Authorize(t.Context(), orderID, tt.amountMinor)
			if tt.wantErr {
				require.ErrorIs(t, err, payments.ErrDeclined)

				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, ref)
		})
	}
}

// Повторная авторизация того же заказа возвращает тот же холд: активность
// Temporal может выполниться дважды, и второй холд был бы деньгами клиента.
func TestMockAuthorizeIsIdempotent(t *testing.T) {
	t.Parallel()

	provider := payments.NewMock()

	first, err := provider.Authorize(t.Context(), orderID, 118900)
	require.NoError(t, err)

	second, err := provider.Authorize(t.Context(), orderID, 118900)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestMockCaptureAndVoid(t *testing.T) {
	t.Parallel()

	provider := payments.NewMock()

	ref, err := provider.Authorize(t.Context(), orderID, 118900)
	require.NoError(t, err)

	require.NoError(t, provider.Capture(t.Context(), ref))
	require.NoError(t, provider.Void(t.Context(), ref))

	require.Error(t, provider.Capture(t.Context(), ""), "списывать нечего")
	require.Error(t, provider.Void(t.Context(), ""), "отменять нечего")
}
