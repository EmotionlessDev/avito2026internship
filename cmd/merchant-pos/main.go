// Команда merchant-pos — пример заведения, подключённого к Авито.Кухне
// (требование задания: заведение отдельным сервисом в том же репозитории).
// Своё хранилище, свой API; с платформой общается только по HTTP-контрактам.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/emotionlessdev/avito-kitchen/internal/pos"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "merchant-pos: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := pos.LoadConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	return pos.Run(ctx, cfg)
}
