package temporalx

import (
	"log/slog"

	"go.temporal.io/api/workflowservice/v1"
)

// Logger переводит вывод Temporal SDK в общий структурный лог: иначе
// сообщения клиента и воркера уходят своим форматом мимо остальных сервисов.
type Logger struct {
	log *slog.Logger
}

// NewLogger оборачивает slog в интерфейс логгера Temporal.
func NewLogger(log *slog.Logger) Logger {
	return Logger{log: log.With(slog.String("component", "temporal"))}
}

// Сообщения приходят из SDK готовыми строками, поэтому они уезжают
// атрибутом, а не текстом записи: так формат лога платформы остаётся
// единым — константное сообщение плюс структурные поля.
const messageKey = "sdk_message"

// Debug пишет отладочное сообщение.
func (l Logger) Debug(msg string, keyvals ...any) {
	l.log.Debug("temporal sdk", append([]any{messageKey, msg}, keyvals...)...)
}

// Info пишет информационное сообщение.
func (l Logger) Info(msg string, keyvals ...any) {
	l.log.Info("temporal sdk", append([]any{messageKey, msg}, keyvals...)...)
}

// Warn пишет предупреждение.
func (l Logger) Warn(msg string, keyvals ...any) {
	l.log.Warn("temporal sdk", append([]any{messageKey, msg}, keyvals...)...)
}

// Error пишет ошибку.
func (l Logger) Error(msg string, keyvals ...any) {
	l.log.Error("temporal sdk", append([]any{messageKey, msg}, keyvals...)...)
}

func systemInfoRequest() *workflowservice.GetSystemInfoRequest {
	return &workflowservice.GetSystemInfoRequest{}
}
