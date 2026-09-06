package merchant

import (
	"context"
	"time"
)

// StopListScheduler возвращает позицию или опцию в продажу по таймеру.
//
// Шов заведён заранее и намеренно: сам таймер — работа Temporal
// (StopListWorkflow, docs/DESIGN.md §6.3), а Temporal подключается на этапе 7.
// Колонки «вернуть в такое-то время» в схеме нет специально — иначе появился
// бы второй источник истины про таймер, и при подключении движка пришлось бы
// решать, кто из них главный.
//
// Пока реализация одна — отказ; на этапе 7 её место занимает клиент Temporal,
// и ни хендлер, ни сервис, ни репозиторий не меняются.
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
