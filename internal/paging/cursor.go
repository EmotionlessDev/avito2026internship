// Package paging — курсоры постраничной выдачи.
//
// Курсор непрозрачен для клиента намеренно: внутри лежит смещение, но клиент
// об этом не знает и не может подставить своё. Когда выдача каталога переедет
// на keyset-пагинацию, содержимое курсора изменится, а контракт — нет.
package paging

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrBadCursor — курсор не разобран: испорчен при передаче или подделан.
var ErrBadCursor = errors.New("некорректный курсор")

// prefix отличает наши курсоры от случайной строки в параметре и заодно
// версионирует формат: сменится схема — сменится префикс.
const prefix = "o1:"

// EncodeOffset упаковывает смещение в курсор.
func EncodeOffset(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(prefix + strconv.Itoa(offset)))
}

// DecodeOffset разбирает курсор. Пустая строка — начало выдачи.
func DecodeOffset(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("%w: не base64", ErrBadCursor)
	}

	value, found := strings.CutPrefix(string(raw), prefix)
	if !found {
		return 0, fmt.Errorf("%w: чужой формат", ErrBadCursor)
	}

	offset, err := strconv.Atoi(value)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("%w: смещение %q", ErrBadCursor, value)
	}

	return offset, nil
}

// Page отрезает от отсортированной выдачи страницу и говорит, есть ли следующая.
// Второе значение — курсор следующей страницы; пустая строка означает, что
// выдача закончилась.
func Page[T any](items []T, offset, limit int) (page []T, nextCursor string) {
	if offset >= len(items) {
		return nil, ""
	}

	end := min(offset+limit, len(items))

	next := ""
	if end < len(items) {
		next = EncodeOffset(end)
	}

	return items[offset:end], next
}
