package httpx

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/emotionlessdev/avito-kitchen/internal/logger"

	"github.com/google/uuid"
)

// HeaderUserID — идентификатор пользователя вместо авторизации.
//
// Осознанное упрощение MVP (README.md, раздел «Осознанные упрощения»): задание разрешает обойтись
// без авторизации. Важно, что хендлеры читают пользователя из контекста,
// поэтому замена заголовка на JWT не тронет ни один из них.
const HeaderUserID = "X-User-Id"

// CodeInvalidUserID — заголовок есть, но это не UUID.
const CodeInvalidUserID = "INVALID_USER_ID"

type userIDKey struct{}

// UserID разбирает заголовок X-User-Id и кладёт пользователя в контекст.
//
// Отсутствие заголовка — не ошибка: каталог и поиск работают анонимно.
// Ручки, которым пользователь нужен (корзина, заказы), спрашивают его
// у контекста сами и отвечают 401, если его нет.
func UserID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(HeaderUserID)
		if raw == "" {
			next.ServeHTTP(w, r)

			return
		}

		id, err := uuid.Parse(raw)
		if err != nil {
			WriteProblem(w, r, NewProblem(http.StatusBadRequest, CodeInvalidUserID,
				"Некорректный идентификатор пользователя",
				HeaderUserID+" должен быть UUID"))

			return
		}

		ctx := context.WithValue(r.Context(), userIDKey{}, id)
		ctx = logger.Into(ctx, logger.From(ctx).With(slog.String("user_id", id.String())))

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserIDFrom достаёт пользователя из контекста.
func UserIDFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey{}).(uuid.UUID)

	return id, ok
}
