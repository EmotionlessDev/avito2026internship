package order

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// StreamOptions — как часто опрашивать историю и когда сдаваться.
type StreamOptions struct {
	// Interval — период опроса. SSE в MVP реализован опросом истории
	// статусов внутри kitchen-api: без подписки на Kafka в HTTP-процессе
	// и без новой инфраструктуры (docs/DESIGN.md §8).
	Interval time.Duration
	// MaxDuration ограничивает жизнь соединения: висящий сокет без событий
	// дороже, чем переподключение клиента.
	MaxDuration time.Duration
}

// DefaultStreamOptions — значения по умолчанию.
func DefaultStreamOptions() StreamOptions {
	return StreamOptions{Interval: 2 * time.Second, MaxDuration: 30 * time.Minute}
}

// Stream отдаёт переходы статуса заказа по мере их появления.
//
// Канал закрывается, когда заказ дошёл до конечного статуса, истёк лимит
// соединения или отменён контекст. Под нагрузкой опрос заменяется
// на LISTEN/NOTIFY или консьюмер order-events — контракт SSE не изменится.
func (s *Service) Stream(
	ctx context.Context, userID, orderID uuid.UUID, options StreamOptions,
) (<-chan StatusChange, error) {
	current, err := s.store.ByUser(ctx, userID, orderID)
	if err != nil {
		return nil, err
	}

	events := make(chan StatusChange, len(current.Timeline)+1)

	// Всё, что уже случилось, отдаём сразу: клиент, подключившийся посреди
	// процесса, должен увидеть полную картину, а не только будущее.
	sent := 0
	for i := range current.Timeline {
		events <- current.Timeline[i]
		sent++
	}

	if IsTerminal(current.Status) {
		close(events)

		return events, nil
	}

	go s.pump(ctx, userID, orderID, sent, options, events)

	return events, nil
}

func (s *Service) pump(
	ctx context.Context, userID, orderID uuid.UUID,
	sent int, options StreamOptions, events chan<- StatusChange,
) {
	defer close(events)

	ticker := time.NewTicker(options.Interval)
	defer ticker.Stop()

	deadline := time.After(options.MaxDuration)

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
		case <-ticker.C:
		}

		current, err := s.store.ByUser(ctx, userID, orderID)
		if err != nil {
			return
		}

		for i := sent; i < len(current.Timeline); i++ {
			select {
			case events <- current.Timeline[i]:
			case <-ctx.Done():
				return
			}
		}

		sent = len(current.Timeline)

		if IsTerminal(current.Status) {
			return
		}
	}
}
