package merchant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/google/uuid"
)

type tenantKey struct{}

// WithTenant кладёт заведение из API-ключа в контекст. Вызывается только
// middleware аутентификации — больше никто не имеет права назначать арендатора.
func WithTenant(ctx context.Context, venueID uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantKey{}, venueID)
}

// TenantFrom достаёт заведение из контекста.
//
// Его спрашивает репозиторий, а не хендлер: venue_id обязан попадать в WHERE
// сам, без участия вызывающего кода. Хендлер, забывший фильтр, не должен
// иметь возможности прочитать чужие данные.
func TenantFrom(ctx context.Context) (uuid.UUID, error) {
	venueID, ok := ctx.Value(tenantKey{}).(uuid.UUID)
	if !ok || venueID == uuid.Nil {
		return uuid.Nil, ErrNoTenant
	}

	return venueID, nil
}

// prefixLength — сколько символов ключа хранится открытым текстом.
// Префикс нужен поддержке («ключ, начинающийся на kitchen_demo…»), а не
// поиску: поиск идёт по хэшу, у которого есть уникальный индекс.
const prefixLength = 12

// HashAPIKey считает отпечаток ключа. В базе лежит только он: утечка дампа
// не даёт доступа к Merchant API.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))

	return hex.EncodeToString(sum[:])
}

// APIKeyPrefix отрезает от ключа опознавательную часть для аудита.
//
// Режем по рунам, а не по байтам: ключи ожидаются ASCII, но обрезанная
// посередине многобайтовая руна превратила бы префикс в невалидный UTF-8
// и сломала бы запись в базу.
func APIKeyPrefix(key string) string {
	runes := []rune(key)
	if len(runes) <= prefixLength {
		return key
	}

	return string(runes[:prefixLength])
}

// BearerToken достаёт ключ из заголовка Authorization.
func BearerToken(header string) (string, bool) {
	const scheme = "Bearer "

	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}

	token := strings.TrimSpace(header[len(scheme):])

	return token, token != ""
}
