package merchant

import (
	"context"
	"time"
)

// StopListScheduler возвращает позицию или опцию в продажу по таймеру.
//
// Шов заведён намеренно: сам таймер — работа Temporal (StopListWorkflow,
// docs/DESIGN.md §6.3). Колонки «вернуть в такое-то время» в схеме нет
// специально — иначе появился бы второй источник истины про таймер,
// и пришлось бы решать, кто из них главный.
//
// Реализаций две: клиент Temporal в рабочем стенде и отказ, если движок
// не подключён. Ни хендлер, ни сервис, ни репозиторий про это не знают.
type StopListScheduler interface {
	// Available сообщает, умеет ли планировщик ставить таймеры вообще.
	// Спрашивается до записи в базу: отказать нужно раньше, чем позиция
	// уедет в стоп-лист, иначе заведение получит 400 уже после того,
	// как блюдо сняли с продажи.
	Available() bool
	// ScheduleReturn ставит возврат в продажу на указанный момент.
	// Реализация обязана быть идемпотентной: повторный вызов для той же
	// цели перезапускает таймер, а не заводит второй.
	ScheduleReturn(ctx context.Context, target StopTarget, until time.Time) error
	// CancelReturn снимает запланированный возврат — заведение вернуло
	// позицию раньше срока.
	CancelReturn(ctx context.Context, target StopTarget) error
}

// UnsupportedScheduler отвечает отказом на любой запрос авто-возврата.
//
// Молча игнорировать поле until нельзя: заведение решило бы, что блюдо
// вернётся само, и оно зависло бы в стоп-листе навсегда. Явный отказ
// заставляет вернуть позицию руками — это работает и сегодня.
type UnsupportedScheduler struct{}

// Available отвечает «нет»: таймеров пока не бывает.
func (UnsupportedScheduler) Available() bool { return false }

// ScheduleReturn всегда отказывает: планировщика ещё нет.
func (UnsupportedScheduler) ScheduleReturn(_ context.Context, _ StopTarget, _ time.Time) error {
	return ErrScheduledReturnUnavailable
}

// CancelReturn ничего не делает: отменять нечего, таймеров пока не бывает.
func (UnsupportedScheduler) CancelReturn(_ context.Context, _ StopTarget) error {
	return nil
}
