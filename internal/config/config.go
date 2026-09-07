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

// Postgres — параметры пула соединений. Postgres здесь единственный источник
// истины (docs/DESIGN.md §5), поэтому пул настраивается явно, а не дефолтами
// драйвера: под нагрузкой упереться в лимит соединений базы дороже, чем
// подождать в очереди за свободным соединением приложения.
type Postgres struct {
	DSN string
	// MaxConns на сервис: суммарно по всем репликам должно оставаться меньше
	// max_connections базы, иначе новый инстанс не поднимется.
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	// ConnectTimeout ограничивает установку соединения: без него старт сервиса
	// с недоступной базой висит до таймаута ядра.
	ConnectTimeout time.Duration
}

// Kafka — параметры брокера событий.
//
// Kafka здесь не критична для чтения каталога: её падение задерживает
// проекции, но не мешает отдавать данные из Postgres. Поэтому таймауты
// короткие — лучше отложить публикацию, чем копить зависшие запросы.
type Kafka struct {
	Brokers []string
	// Partitions и ReplicationFactor применяются только при создании топиков.
	// В стенде брокер один, поэтому и фактор репликации — 1.
	Partitions        int32
	ReplicationFactor int16
	MaxMessageBytes   int32
	Timeout           time.Duration
}

// Temporal — подключение к движку оркестрации.
type Temporal struct {
	HostPort  string
	Namespace string
	TaskQueue string
	// ConnectRetryInterval — пауза между попытками подключения на старте:
	// auto-setup в стенде поднимается около сорока секунд.
	ConnectRetryInterval time.Duration
	// ConfirmationTimeout — сколько заказ ждёт ответа заведения. Вынесен
	// в конфигурацию ради демо и e2e: ждать семь минут в прогоне сценариев
	// незачем, а поведение процесса от величины срока не зависит.
	ConfirmationTimeout time.Duration
	CookingSLA          time.Duration
	DeliverySLA         time.Duration
}

// Outbox — параметры доставки событий из базы в брокер.
type Outbox struct {
	BatchSize    int
	PollInterval time.Duration
	MaxAttempts  int
}

// KitchenAPI — конфигурация HTTP-сервиса (public + merchant контуры).
type KitchenAPI struct {
	App
	HTTP     HTTP
	Postgres Postgres
	Temporal Temporal
}

// KitchenWorker — конфигурация воркера. Свой HTTP-сервер он поднимает только
// ради healthcheck'ов: Temporal и Kafka появятся здесь на этапах 6–7.
type KitchenWorker struct {
	App
	HTTP     HTTP
	Postgres Postgres
	Kafka    Kafka
	Outbox   Outbox
	Temporal Temporal
}

// LoadKitchenAPI читает конфигурацию kitchen-api.
func LoadKitchenAPI() (KitchenAPI, error) {
	l := &loader{}
	cfg := KitchenAPI{
		App:      l.app("kitchen-api"),
		HTTP:     l.http(":8080"),
		Postgres: l.postgres(),
		Temporal: l.temporal(),
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
		App:      l.app("kitchen-worker"),
		HTTP:     l.http(":8081"),
		Postgres: l.postgres(),
		Kafka:    l.kafka(),
		Outbox:   l.outbox(),
		Temporal: l.temporal(),
	}

	if err := l.err(); err != nil {
		return KitchenWorker{}, fmt.Errorf("конфигурация kitchen-worker: %w", err)
	}

	return cfg, nil
}

// Migrator — конфигурация одноразового мигратора. HTTP-сервера у него нет:
// он накатывает миграции и завершается, compose ждёт его кода возврата.
type Migrator struct {
	App
	Postgres Postgres
	// Seed включает наполнение базы демо-данными сразу после миграций —
	// так стенд из чистого клона поднимается уже с каталогом.
	Seed bool
}

// LoadMigrator читает конфигурацию мигратора.
func LoadMigrator() (Migrator, error) {
	l := &loader{}
	cfg := Migrator{
		App:      l.app("migrator"),
		Postgres: l.postgres(),
		Seed:     l.boolean("MIGRATOR_SEED", false),
	}

	if err := l.err(); err != nil {
		return Migrator{}, fmt.Errorf("конфигурация migrator: %w", err)
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

func (l *loader) postgres() Postgres {
	return Postgres{
		// Дефолт указывает на порт стенда с хоста (docker-compose.yml),
		// чтобы `go run ./cmd/...` работал без единой заданной переменной.
		DSN:             l.str("POSTGRES_DSN", "postgres://kitchen:kitchen@localhost:55432/kitchen?sslmode=disable"),
		MaxConns:        l.int32("POSTGRES_MAX_CONNS", 10),
		MinConns:        l.int32("POSTGRES_MIN_CONNS", 2),
		MaxConnLifetime: l.duration("POSTGRES_MAX_CONN_LIFETIME", time.Hour),
		MaxConnIdleTime: l.duration("POSTGRES_MAX_CONN_IDLE_TIME", 30*time.Minute),
		ConnectTimeout:  l.duration("POSTGRES_CONNECT_TIMEOUT", 5*time.Second),
	}
}

func (l *loader) kafka() Kafka {
	return Kafka{
		// Дефолт указывает на внешний listener стенда, чтобы воркер можно
		// было запустить с хоста; в compose адрес переопределяется.
		Brokers:           l.list("KAFKA_BROKERS", []string{"localhost:9094"}),
		Partitions:        l.int32("KAFKA_PARTITIONS", 3),
		ReplicationFactor: l.int16("KAFKA_REPLICATION_FACTOR", 1),
		MaxMessageBytes:   l.int32("KAFKA_MAX_MESSAGE_BYTES", 1_048_576),
		Timeout:           l.duration("KAFKA_TIMEOUT", 10*time.Second),
	}
}

func (l *loader) outbox() Outbox {
	return Outbox{
		BatchSize: int(l.int32("OUTBOX_BATCH_SIZE", 100)),
		// Опрос — страховка: основной сигнал приходит через LISTEN/NOTIFY,
		// поэтому интервал может быть спокойным.
		PollInterval: l.duration("OUTBOX_POLL_INTERVAL", 2*time.Second),
		MaxAttempts:  int(l.int32("OUTBOX_MAX_ATTEMPTS", 5)),
	}
}

func (l *loader) temporal() Temporal {
	return Temporal{
		HostPort:  l.str("TEMPORAL_HOSTPORT", "localhost:7233"),
		Namespace: l.str("TEMPORAL_NAMESPACE", "default"),
		// Очередь одна на все процессы заказа: разделять по типам пока нечего.
		TaskQueue:            l.str("TEMPORAL_TASK_QUEUE", "kitchen-orders"),
		ConnectRetryInterval: l.duration("TEMPORAL_CONNECT_RETRY_INTERVAL", 2*time.Second),
		ConfirmationTimeout:  l.duration("ORDER_CONFIRMATION_TIMEOUT", 7*time.Minute),
		CookingSLA:           l.duration("ORDER_COOKING_SLA", 30*time.Minute),
		DeliverySLA:          l.duration("ORDER_DELIVERY_SLA", 45*time.Minute),
	}
}
