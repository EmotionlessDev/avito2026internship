// Package pos — эмулятор заведения, подключённого к платформе.
//
// Это отдельная система, а не модуль платформы: со своим хранилищем (SQLite,
// этап 8) и своим бутстрапом. С платформой общается только по HTTP-контрактам
// из api/, поэтому платформенные пакеты internal/* здесь не импортируются —
// граница закреплена правилом depguard в .golangci.yml.
//
// Именно поэтому http-сервер и разбор окружения тут свои, а не переиспользованы
// из internal/httpx: дублирование через границу двух систем — не копипаста,
// а отсутствие связанности. Реальный POS заведения тоже не линкуется с нашим кодом.
package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// Config — конфигурация сервиса заведения.
type Config struct {
	Addr            string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	// PlatformBaseURL — адрес Merchant API платформы. Используется с этапа 8:
	// импорт меню, регистрация вебхука, поллинг-фолбэк.
	PlatformBaseURL string
}

// LoadConfig читает конфигурацию из окружения с рабочими значениями по умолчанию.
func LoadConfig() (Config, error) {
	cfg := Config{
		Addr:            envStr("POS_HTTP_ADDR", ":8090"),
		LogLevel:        slog.LevelInfo,
		ShutdownTimeout: 15 * time.Second,
		PlatformBaseURL: envStr("POS_PLATFORM_BASE_URL", "http://kitchen-api:8080"),
	}

	if raw := os.Getenv("POS_LOG_LEVEL"); raw != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			return Config{}, fmt.Errorf("POS_LOG_LEVEL=%q: %w", raw, err)
		}
	}

	if raw := os.Getenv("POS_SHUTDOWN_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("POS_SHUTDOWN_TIMEOUT=%q: %w", raw, err)
		}

		cfg.ShutdownTimeout = d
	}

	return cfg, nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

// Run поднимает сервис заведения и работает до отмены контекста.
func Run(ctx context.Context, cfg Config) error {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With(slog.String("service", "merchant-pos"))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           newRouter(log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)

	go func() {
		log.InfoContext(ctx, "merchant-pos слушает",
			slog.String("addr", cfg.Addr),
			slog.String("platform", cfg.PlatformBaseURL))

		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen %s: %w", cfg.Addr, err)

			return
		}

		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown %s: %w", cfg.Addr, err)
	}

	return <-errCh
}

// newRouter отдаёт маршруты POS. На этапе 0 это только пробы; приём вебхуков,
// операторские ручки accept/reject/ready и сценарные триггеры — этап 8.
func newRouter(log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(r.Context(), log, w, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(r.Context(), log, w, map[string]string{"status": "ok"})
	})

	return mux
}

func writeJSON(ctx context.Context, log *slog.Logger, w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.WarnContext(ctx, "не удалось записать тело ответа", slog.String("error", err.Error()))
	}
}
