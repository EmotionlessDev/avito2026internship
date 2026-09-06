package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/config"

	"github.com/go-chi/chi/v5"
)

// Server — http.Server с корректным завершением по отмене контекста.
type Server struct {
	srv             *http.Server
	log             *slog.Logger
	shutdownTimeout time.Duration
}

// NewServer собирает HTTP-сервер с явными таймаутами из конфигурации.
func NewServer(httpCfg config.HTTP, app config.App, handler http.Handler, log *slog.Logger) *Server {
	return &Server{
		srv: &http.Server{
			Addr:              httpCfg.Addr,
			Handler:           handler,
			ReadHeaderTimeout: httpCfg.ReadHeaderTimeout,
			ReadTimeout:       httpCfg.ReadTimeout,
			WriteTimeout:      httpCfg.WriteTimeout,
			IdleTimeout:       httpCfg.IdleTimeout,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		},
		log:             log,
		shutdownTimeout: app.ShutdownTimeout,
	}
}

// Run слушает адрес до отмены ctx, затем гасит сервер gracefully: перестаёт
// принимать новые соединения и даёт начатым запросам доиграть в пределах
// ShutdownTimeout. Возвращает nil, если завершение штатное.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		s.log.InfoContext(ctx, "http-сервер слушает", slog.String("addr", s.srv.Addr))

		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen %s: %w", s.srv.Addr, err)

			return
		}

		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// Контекст завершения намеренно отвязан от ctx: тот уже отменён,
	// а Shutdown с отменённым контекстом рвёт соединения мгновенно.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()

	s.log.InfoContext(ctx, "останавливаю http-сервер",
		slog.Duration("timeout", s.shutdownTimeout))

	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown %s: %w", s.srv.Addr, err)
	}

	return <-errCh
}

// NewRouter собирает роутер с базовой цепочкой middleware и health-ручками.
// Порядок важен: request-id проставляется первым, чтобы попасть и в access-лог,
// и в лог паники; Recover стоит внутри AccessLog, поэтому упавший запрос
// всё равно попадает в лог — со статусом 500.
func NewRouter(log *slog.Logger, health *Health) *chi.Mux {
	r := chi.NewRouter()

	r.Use(RequestID)
	r.Use(RequestPath)
	r.Use(AccessLog(log))
	r.Use(Recover)

	r.Get("/healthz", health.Live)
	r.Get("/readyz", health.Ready)

	return r
}
