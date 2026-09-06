package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// loader читает переменные окружения, накапливая ошибки разбора вместо того,
// чтобы падать на первой. Сервис, поднятый с тремя опечатками в env, должен
// сообщить обо всех трёх сразу, а не заставлять чинить их по одной.
type loader struct {
	errs []error
}

func (l *loader) fail(key, raw string, err error) {
	l.errs = append(l.errs, fmt.Errorf("%s=%q: %w", key, raw, err))
}

func (l *loader) str(key, def string) string {
	if raw, ok := os.LookupEnv(key); ok && raw != "" {
		return raw
	}

	return def
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}

	v, err := time.ParseDuration(raw)
	if err != nil {
		l.fail(key, raw, err)

		return def
	}

	return v
}

func (l *loader) logLevel(key string, def slog.Level) slog.Level {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}

	var v slog.Level
	if err := v.UnmarshalText([]byte(raw)); err != nil {
		l.fail(key, raw, err)

		return def
	}

	return v
}

func (l *loader) oneOf(key, def string, allowed ...string) string {
	v := l.str(key, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}

	l.fail(key, v, fmt.Errorf("%w: ожидается одно из %v", errUnexpectedValue, allowed))

	return def
}

func (l *loader) err() error {
	return errors.Join(l.errs...)
}

func (l *loader) int32(key string, def int32) int32 {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}

	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		l.fail(key, raw, err)

		return def
	}

	return int32(v)
}

func (l *loader) boolean(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}

	v, err := strconv.ParseBool(raw)
	if err != nil {
		l.fail(key, raw, err)

		return def
	}

	return v
}

var errUnexpectedValue = errors.New("недопустимое значение")
