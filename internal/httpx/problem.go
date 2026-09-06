package httpx

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/emotionlessdev/avito-kitchen/internal/logger"
)

// ProblemContentType — тип ответа с ошибкой по RFC 9457.
const ProblemContentType = "application/problem+json"

// typeBaseURL — префикс поля type. Ссылка не обязана резолвиться в браузере,
// но обязана быть стабильной: по ней клиенты различают классы ошибок.
const typeBaseURL = "https://kitchen.avito.ru/errors/"

// Problem — тело ошибки по RFC 9457 с полем code.
//
// Свой тип, а не сгенерированный: этот нужен там, где сгенерированного кода
// ещё нет — в middleware и в обработчике ошибок роутера.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Code     string `json:"code"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// NewProblem собирает ошибку, выводя type из кода: VENUE_NOT_FOUND
// превращается в .../errors/venue-not-found. Так поле type не нужно
// придумывать заново на каждой ошибке и оно не разъезжается с кодом.
func NewProblem(status int, code, title, detail string) Problem {
	return Problem{
		Type:   typeBaseURL + strings.ToLower(strings.ReplaceAll(code, "_", "-")),
		Title:  title,
		Status: status,
		Code:   code,
		Detail: detail,
	}
}

// WriteProblem отправляет ошибку клиенту, подставляя путь запроса в instance.
func WriteProblem(w http.ResponseWriter, r *http.Request, problem Problem) {
	if problem.Instance == "" {
		problem.Instance = r.URL.Path
	}

	if err := EncodeProblem(w, problem); err != nil {
		logger.From(r.Context()).WarnContext(r.Context(), "не удалось записать problem+json",
			slog.String("error", err.Error()))
	}
}

// EncodeProblem пишет ошибку туда, где запроса под рукой нет — например,
// в сгенерированных strict-ответах, которые получают только ResponseWriter.
func EncodeProblem(w http.ResponseWriter, problem Problem) error {
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(problem.Status)

	if err := json.NewEncoder(w).Encode(problem); err != nil {
		return fmt.Errorf("запись problem+json: %w", err)
	}

	return nil
}

// Коды ошибок, которые платформа возвращает вне доменных сервисов.
const (
	// CodeBadRequest — запрос не разобран: не тот тип параметра, не тот JSON.
	CodeBadRequest = "BAD_REQUEST"
	// CodeInternal — всё остальное. Наружу деталей не отдаём, они уходят в лог.
	CodeInternal = "INTERNAL_ERROR"
)

// BadRequest — стандартный ответ на неразобранный запрос.
func BadRequest(w http.ResponseWriter, r *http.Request, detail string) {
	WriteProblem(w, r, NewProblem(http.StatusBadRequest, CodeBadRequest, "Некорректный запрос", detail))
}

// Internal — ответ на внутреннюю ошибку. Причина остаётся в логе:
// наружу она может утечь деталями схемы или запроса.
func Internal(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, NewProblem(
		http.StatusInternalServerError, CodeInternal, "Внутренняя ошибка", ""))
}
