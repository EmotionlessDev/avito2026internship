package pos

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// runPoller забирает заказы, которые не доехали вебхуком.
//
// Это страховка сценария M6: вебхук мог не дойти, пока заведение было
// недоступно, а заказ терять нельзя. Дублей не будет — заказ кладётся
// по идентификатору платформы, и повторная запись ничего не меняет.
func (s *Service) runPoller(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	s.log.InfoContext(ctx, "поллинг-фолбэк включён",
		slog.Duration("interval", s.cfg.PollInterval))

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if s.offline() {
			continue
		}

		s.poll(ctx)
	}
}

func (s *Service) poll(ctx context.Context) {
	since := s.pollSince()

	orders, err := s.platform.NewOrders(ctx, since)
	if err != nil {
		s.log.DebugContext(ctx, "поллинг не удался", slog.String("error", err.Error()))

		return
	}

	now := time.Now().UTC()
	s.rememberPoll(now)

	for i := range orders {
		payload, err := json.Marshal(orders[i])
		if err != nil {
			continue
		}

		saved, err := s.storage.SaveOrder(ctx, Order{
			ID:         orders[i].Id.String(),
			Number:     orders[i].Number,
			Status:     statusNew,
			Payload:    string(payload),
			ReceivedAt: now,
			ActAfter:   now.Add(s.cfg.AcceptDelay),
		})
		if err != nil {
			s.log.ErrorContext(ctx, "не удалось сохранить заказ из поллинга",
				slog.String("error", err.Error()))

			continue
		}

		if saved {
			s.log.InfoContext(ctx, "заказ подобран поллингом",
				slog.String("order", orders[i].Number))
		}
	}
}

// pollOverlap — насколько отсечка сдвигается назад относительно прошлого тика.
//
// Часы платформы и заведения расходятся, а заказ, попавший в ленту ровно
// в момент отсечки, при строгом сравнении провалился бы между тиками.
// Перечитать десяток заказов дешевле, чем потерять один: приём заказов
// идемпотентен по идентификатору платформы.
const pollOverlap = 30 * time.Second

// pollSince отдаёт отсечку для очередного тика. Пустая означает «всю ленту»:
// так заведение читает её на первом тике и после перезапуска.
func (s *Service) pollSince() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastPoll.IsZero() {
		return nil
	}

	since := s.lastPoll.Add(-pollOverlap)

	return &since
}

func (s *Service) rememberPoll(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastPoll = at
}
