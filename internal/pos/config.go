package pos

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Config — конфигурация сервиса заведения.
//
// Своя, а не общая с платформой: POS — отдельная система, и переменные
// окружения у него собственные, с префиксом POS_.
type Config struct {
	Addr            string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration

	// PlatformBaseURL — адрес платформы: туда уходят импорт меню,
	// регистрация вебхука, подтверждения заказов и поллинг-фолбэк.
	PlatformBaseURL string
	// APIKey — ключ Merchant API, выданный заведению.
	APIKey string

	// WebhookURL — адрес, который POS сообщает платформе. В compose это его
	// имя в сети docker; снаружи он был бы публичным адресом заведения.
	WebhookURL string
	// WebhookSecret — общий секрет подписи. Платформа подписывает им запросы,
	// POS этой же подписью их проверяет.
	WebhookSecret string
	// WebhookMaxSkew — окно, в котором принимается запрос. Старый запрос
	// с валидной подписью — это переигранный перехват, а не наш вызов.
	WebhookMaxSkew time.Duration

	// DatabasePath — файл SQLite. Заведение хранит заказы у себя, а не
	// в базе платформы: это разные системы.
	DatabasePath string

	// Autopilot включает автоматическую работу «повара»: заказ принимается,
	// готовится и отдаётся сам. Нужен демо — иначе каждый прогон требовал бы
	// человека, нажимающего кнопки.
	Autopilot bool
	// AcceptDelay — пауза перед подтверждением: повар не мгновенный.
	AcceptDelay time.Duration
	// PrepTime — сколько «готовится» заказ.
	PrepTime time.Duration
	// DeliveryTime — сколько «едет» курьер до подтверждения доставки.
	DeliveryTime time.Duration

	// PollInterval — как часто спрашивать платформу о новых заказах.
	// Это страховка на случай, если вебхук не дошёл (сценарий M6).
	PollInterval time.Duration
}

// LoadConfig читает конфигурацию из окружения с рабочими значениями
// по умолчанию: `docker compose up` должен работать без единой переменной.
func LoadConfig() (Config, error) {
	cfg := Config{
		Addr:            envStr("POS_HTTP_ADDR", ":8090"),
		LogLevel:        slog.LevelInfo,
		ShutdownTimeout: 15 * time.Second,
		PlatformBaseURL: envStr("POS_PLATFORM_BASE_URL", "http://kitchen-api:8080"),
		APIKey:          envStr("POS_API_KEY", "kitchen_demo_hachapuri_key"),
		WebhookURL:      envStr("POS_WEBHOOK_URL", "http://merchant-pos:8090/pos/webhooks/orders"),
		WebhookSecret:   envStr("POS_WEBHOOK_SECRET", "pos-demo-secret-0123456789"),
		WebhookMaxSkew:  5 * time.Minute,
		DatabasePath:    envStr("POS_DATABASE_PATH", "/data/pos.db"),
		Autopilot:       true,
		AcceptDelay:     3 * time.Second,
		PrepTime:        10 * time.Second,
		DeliveryTime:    10 * time.Second,
		PollInterval:    5 * time.Second,
	}

	if raw := os.Getenv("POS_LOG_LEVEL"); raw != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			return Config{}, fmt.Errorf("POS_LOG_LEVEL=%q: %w", raw, err)
		}
	}

	if raw := os.Getenv("POS_AUTOPILOT"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("POS_AUTOPILOT=%q: %w", raw, err)
		}

		cfg.Autopilot = enabled
	}

	durations := map[string]*time.Duration{
		"POS_SHUTDOWN_TIMEOUT": &cfg.ShutdownTimeout,
		"POS_ACCEPT_DELAY":     &cfg.AcceptDelay,
		"POS_PREP_TIME":        &cfg.PrepTime,
		"POS_DELIVERY_TIME":    &cfg.DeliveryTime,
		"POS_POLL_INTERVAL":    &cfg.PollInterval,
		"POS_WEBHOOK_MAX_SKEW": &cfg.WebhookMaxSkew,
	}

	for key, target := range durations {
		raw := os.Getenv(key)
		if raw == "" {
			continue
		}

		value, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q: %w", key, raw, err)
		}

		*target = value
	}

	return cfg, nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
