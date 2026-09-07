package pos

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// Имена полей в ответах POS. Свой маленький словарь: у заведения нет
// problem+json платформы, а повторять строковые литералы по коду — верный
// способ однажды опечататься.
const (
	fieldError      = "error"
	fieldScenario   = "scenario"
	fieldExternalID = "external_id"
)

// maxWebhookBody ограничивает тело вебхука: подпись считается по всему телу,
// и читать его в память нужно целиком — значит, нужен предел.
const maxWebhookBody = 1 << 20 // 1 МиБ

// drainBody читает тело запроса и возвращает его на место, чтобы дальше
// по цепочке хендлер получил нетронутый запрос.
func drainBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		return nil, fmt.Errorf("чтение тела запроса: %w", err)
	}

	if err := r.Body.Close(); err != nil {
		return nil, fmt.Errorf("закрытие тела запроса: %w", err)
	}

	r.Body = io.NopCloser(bytes.NewReader(body))

	return body, nil
}
