package pos

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// runAutopilot ведёт заказы за повара: подтверждает, готовит, отдаёт.
//
// Без него демо требовало бы человека, который вовремя нажимает кнопки.
// Автопилот выключается переменной POS_AUTOPILOT=false — тогда заказами
// управляют операторские ручки, как в жизни.
func (s *Service) runAutopilot(ctx context.Context) {
	const tick = time.Second

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	s.log.InfoContext(ctx, "автопилот включён",
		slog.Duration("accept_delay", s.cfg.AcceptDelay),
		slog.Duration("prep_time", s.cfg.PrepTime))

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Пока заведение «недоступно», кухня не работает: это ровно то,
		// что проверяет сценарий M6.
		if s.offline() {
			continue
		}

		s.step(ctx)
	}
}

// step двигает заказы на один шаг: новые — подтверждает, приготовленные —
// объявляет готовыми, готовые — отдаёт.
func (s *Service) step(ctx context.Context) {
	now := time.Now().UTC()

	s.advance(ctx, statusNew, now, s.decideNewOrder)
	s.advance(ctx, statusAccepted, now, s.markReady)
	s.advance(ctx, statusReady, now, s.markDelivered)
}

func (s *Service) advance(
	ctx context.Context, status string, now time.Time, action func(context.Context, Order) error,
) {
	orders, err := s.storage.OrdersDue(ctx, status, now)
	if err != nil {
		s.log.ErrorContext(ctx, "не удалось прочитать заказы",
			slog.String("status", status), slog.String("error", err.Error()))

		return
	}

	for i := range orders {
		if err := action(ctx, orders[i]); err != nil {
			s.log.ErrorContext(ctx, "шаг автопилота не удался",
				slog.String("order", orders[i].Number),
				slog.String("error", err.Error()))
		}
	}
}

// decideNewOrder решает судьбу нового заказа: принять, отклонить или
// промолчать — в зависимости от сценарных триггеров.
func (s *Service) decideNewOrder(ctx context.Context, order Order) error {
	orderID, err := uuid.Parse(order.ID)
	if err != nil {
		return err
	}

	reject, ignore := s.takeDecision()

	switch {
	case ignore:
		// Заказ остаётся новым и без ответа: платформа отменит его
		// по таймеру подтверждения (сценарий U10).
		s.log.InfoContext(ctx, "сценарий: заказ оставлен без ответа",
			slog.String("order", order.Number))

		return s.storage.SetStatus(ctx, order.ID, statusNew, time.Now().UTC().Add(time.Hour))
	case reject:
		if err := s.platform.Reject(ctx, orderID, "закончились продукты"); err != nil {
			return s.handleTransitionError(ctx, order, err)
		}

		s.log.InfoContext(ctx, "заказ отклонён", slog.String("order", order.Number))

		return s.storage.SetStatus(ctx, order.ID, statusRejected, time.Now().UTC())
	default:
		if err := s.platform.Accept(ctx, orderID, etaMinutes(s.cfg)); err != nil {
			return s.handleTransitionError(ctx, order, err)
		}

		s.log.InfoContext(ctx, "заказ принят", slog.String("order", order.Number))

		return s.storage.SetStatus(ctx, order.ID, statusAccepted, time.Now().UTC().Add(s.cfg.PrepTime))
	}
}

func (s *Service) markReady(ctx context.Context, order Order) error {
	orderID, err := uuid.Parse(order.ID)
	if err != nil {
		return err
	}

	if err := s.platform.Ready(ctx, orderID); err != nil {
		return s.handleTransitionError(ctx, order, err)
	}

	s.log.InfoContext(ctx, "заказ готов", slog.String("order", order.Number))

	return s.storage.SetStatus(ctx, order.ID, statusReady, time.Now().UTC().Add(s.cfg.DeliveryTime))
}

func (s *Service) markDelivered(ctx context.Context, order Order) error {
	orderID, err := uuid.Parse(order.ID)
	if err != nil {
		return err
	}

	if err := s.platform.Delivered(ctx, orderID); err != nil {
		return s.handleTransitionError(ctx, order, err)
	}

	s.log.InfoContext(ctx, "заказ доставлен", slog.String("order", order.Number))

	return s.storage.SetStatus(ctx, order.ID, statusDelivered, time.Now().UTC())
}

// handleTransitionError разбирает отказ платформы.
//
// 409 означает, что заказ уже ушёл дальше без нас: пользователь отменил его
// или сработал таймаут. Для кухни это не ошибка — заказ просто снимается
// с производства, и повторять попытку не нужно.
func (s *Service) handleTransitionError(ctx context.Context, order Order, err error) error {
	var unexpected errUnexpectedStatus
	if errors.As(err, &unexpected) && unexpected.status == 409 {
		s.log.InfoContext(ctx, "платформа уже перевела заказ дальше",
			slog.String("order", order.Number))

		return s.storage.SetStatus(ctx, order.ID, statusCancelled, time.Now().UTC())
	}

	return err
}
