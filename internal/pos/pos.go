// Package pos — эмулятор заведения, подключённого к платформе.
//
// Это отдельная система, а не модуль платформы: со своим хранилищем (SQLite),
// своим бутстрапом и своими переменными окружения. С платформой общается
// только по HTTP-контрактам из api/, поэтому платформенные пакеты internal/*
// здесь не импортируются — граница закреплена правилом depguard
// в .golangci.yml.
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
	"sync"
	"time"
)

// Service — сервис заведения: хранилище, клиент платформы, состояние сценариев.
type Service struct {
	cfg      Config
	log      *slog.Logger
	storage  *Storage
	platform *Platform

	categories   []menuCategory
	optionGroups []menuOptionGroup

	// scenario — состояние сценарных триггеров. Оно живёт в памяти:
	// это инструмент демо, а не данные заведения, и переживать перезапуск
	// ему незачем.
	mu       sync.Mutex
	scenario scenarioState
}

// Run поднимает сервис заведения и работает до отмены контекста.
func Run(ctx context.Context, cfg Config) error {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With(slog.String("service", "merchant-pos"))

	storage, err := OpenStorage(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}

	defer func() { _ = storage.Close() }()

	platform, err := NewPlatform(cfg.PlatformBaseURL, cfg.APIKey)
	if err != nil {
		return err
	}

	service := &Service{cfg: cfg, log: log, storage: storage, platform: platform}

	if err := service.seedMenu(ctx); err != nil {
		return err
	}

	// Онбординг — то же, что делает настоящее заведение при подключении:
	// отдаёт меню и говорит, куда слать заказы (сценарий M1).
	go service.onboard(ctx)

	if cfg.Autopilot {
		go service.runAutopilot(ctx)
	}

	go service.runPoller(ctx)

	return service.serve(ctx)
}

// onboard регистрирует заведение на платформе.
//
// В отдельной горутине и с повторами: POS стартует рядом с платформой,
// и первые секунды она может быть ещё не готова. Падать из-за этого сервису
// незачем — он попробует снова.
func (s *Service) onboard(ctx context.Context) {
	const retryInterval = 3 * time.Second

	for attempt := 1; ctx.Err() == nil; attempt++ {
		if err := s.syncMenu(ctx); err != nil {
			s.log.WarnContext(ctx, "меню не загружено в платформу",
				slog.Int("attempt", attempt), slog.String("error", err.Error()))
		} else if err := s.platform.RegisterWebhook(ctx, s.cfg.WebhookURL, s.cfg.WebhookSecret); err != nil {
			s.log.WarnContext(ctx, "вебхук не зарегистрирован",
				slog.Int("attempt", attempt), slog.String("error", err.Error()))
		} else {
			s.log.InfoContext(ctx, "заведение подключено к платформе",
				slog.String("webhook", s.cfg.WebhookURL))

			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(retryInterval):
		}
	}
}

// syncMenu отправляет платформе текущее меню заведения.
func (s *Service) syncMenu(ctx context.Context) error {
	items, err := s.storage.Menu(ctx)
	if err != nil {
		return err
	}

	return s.platform.ImportMenu(ctx, items, s.categories, s.optionGroups)
}

func (s *Service) serve(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.router(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)

	go func() {
		s.log.InfoContext(ctx, "merchant-pos слушает",
			slog.String("addr", s.cfg.Addr),
			slog.String("platform", s.cfg.PlatformBaseURL),
			slog.Bool("autopilot", s.cfg.Autopilot))

		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen %s: %w", s.cfg.Addr, err)

			return
		}

		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown %s: %w", s.cfg.Addr, err)
	}

	return <-errCh
}

func writeJSON(ctx context.Context, log *slog.Logger, w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.WarnContext(ctx, "не удалось записать тело ответа", slog.String("error", err.Error()))
	}
}
