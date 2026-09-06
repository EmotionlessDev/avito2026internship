// Package config собирает конфигурацию платформенных сервисов из переменных
// окружения: один вызов Load на старте, дальше конфиг неизменяем.
//
// Дефолты подобраны так, чтобы сервис поднимался без единой заданной переменной —
// это нужно для `docker compose up` из чистого репозитория и для тестов.
package config

import (
	"fmt"
	"log/slog"
	"time"
)

// Окружения, влияющие на формат логов и подробность ошибок.
const (
	EnvDev  = "dev"
	EnvProd = "prod"
)

// Форматы вывода логов.
const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

// App — общая часть конфигурации, одинаковая у всех бинарей платформы.
type App struct {
	// Env влияет на дефолтный формат логов и на то, отдаём ли мы наружу детали ошибок.
	Env string
	// ServiceName попадает в каждую строку лога — в compose их несколько.
	ServiceName string
	LogLevel    slog.Level
	LogFormat   string
	// ShutdownTimeout ограничивает graceful shutdown: сколько ждём завершения
	// начатых запросов, прежде чем закрыть соединения принудительно.
	ShutdownTimeout time.Duration
}

// IsDev сообщает, работаем ли мы в dev-окружении.
func (a App) IsDev() bool { return a.Env == EnvDev }

// HTTP — параметры http.Server. Таймауты заданы явно: сервер без них
// держит соединение бесконечно и утекает под медленным клиентом.
type HTTP struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// KitchenAPI — конфигурация HTTP-сервиса (public + merchant контуры).
type KitchenAPI struct {
	App
	HTTP HTTP
}

// KitchenWorker — конфигурация воркера. Свой HTTP-сервер он поднимает только
// ради healthcheck'ов: Temporal и Kafka появятся здесь на этапах 6–7.
type KitchenWorker struct {
	App
	HTTP HTTP
}

// LoadKitchenAPI читает конфигурацию kitchen-api.
func LoadKitchenAPI() (KitchenAPI, error) {
	l := &loader{}
	cfg := KitchenAPI{
		App:  l.app("kitchen-api"),
		HTTP: l.http(":8080"),
	}

	if err := l.err(); err != nil {
		return KitchenAPI{}, fmt.Errorf("конфигурация kitchen-api: %w", err)
	}

	return cfg, nil
}

// LoadKitchenWorker читает конфигурацию kitchen-worker.
func LoadKitchenWorker() (KitchenWorker, error) {
	l := &loader{}
	cfg := KitchenWorker{
		App:  l.app("kitchen-worker"),
		HTTP: l.http(":8081"),
	}

	if err := l.err(); err != nil {
		return KitchenWorker{}, fmt.Errorf("конфигурация kitchen-worker: %w", err)
	}

	return cfg, nil
}

func (l *loader) app(service string) App {
	env := l.oneOf("APP_ENV", EnvDev, EnvDev, EnvProd)

	// В dev человекочитаемый текст, в prod — JSON для сборщика логов.
	defaultFormat := LogFormatJSON
	if env == EnvDev {
		defaultFormat = LogFormatText
	}

	return App{
		Env:             env,
		ServiceName:     service,
		LogLevel:        l.logLevel("LOG_LEVEL", slog.LevelInfo),
		LogFormat:       l.oneOf("LOG_FORMAT", defaultFormat, LogFormatJSON, LogFormatText),
		ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
	}
}

func (l *loader) http(defaultAddr string) HTTP {
	return HTTP{
		Addr:              l.str("HTTP_ADDR", defaultAddr),
		ReadHeaderTimeout: l.duration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       l.duration("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:      l.duration("HTTP_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:       l.duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
	}
}
