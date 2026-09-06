package catalog

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Weekday — день недели в нумерации базы: 0 — понедельник, 6 — воскресенье.
// Своя нумерация, а не time.Weekday, потому что в расписании заведений неделя
// начинается с понедельника, и хранить в базе воскресенье нулём было бы
// источником путаницы при чтении дампа глазами.
type Weekday int

// Дни недели в нумерации venue_work_hours.
const (
	Monday Weekday = iota
	Tuesday
	Wednesday
	Thursday
	Friday
	Saturday
	Sunday
)

const daysInWeek = 7

// WeekdayOf переводит момент времени в нумерацию расписания.
func WeekdayOf(t time.Time) Weekday {
	// time.Sunday == 0, у нас воскресенье — 6.
	return Weekday((int(t.Weekday()) + daysInWeek - 1) % daysInWeek)
}

func (d Weekday) prev() Weekday {
	return (d + daysInWeek - 1) % daysInWeek
}

// TimeOfDay — время суток в минутах от полуночи. Целые минуты вместо
// time.Duration: расписание заведения не бывает точнее минуты, а сравнивать
// и сериализовать целые проще.
type TimeOfDay int

// ErrBadTimeOfDay — время суток не разобрано: в базе или в запросе лежит
// не «ЧЧ:ММ».
var ErrBadTimeOfDay = errors.New("некорректное время суток")

// ParseTimeOfDay разбирает «ЧЧ:ММ» и «ЧЧ:ММ:СС»: первый формат приходит
// из API, второй — из колонки time базы. Секунды в расписании не значимы
// и отбрасываются.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("%w: %q, ожидается ЧЧ:ММ", ErrBadTimeOfDay, s)
	}

	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("%w: часы в %q", ErrBadTimeOfDay, s)
	}

	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("%w: минуты в %q", ErrBadTimeOfDay, s)
	}

	if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return 0, fmt.Errorf("%w: %q вне суток", ErrBadTimeOfDay, s)
	}

	return TimeOfDay(hours*60 + minutes), nil
}

// TimeOfDayAt возвращает время суток момента t в его зоне.
func TimeOfDayAt(t time.Time) TimeOfDay {
	return TimeOfDay(t.Hour()*60 + t.Minute())
}

// String печатает время суток как «ЧЧ:ММ».
func (t TimeOfDay) String() string {
	return fmt.Sprintf("%02d:%02d", int(t)/60, int(t)%60)
}

// WorkInterval — один интервал работы в конкретный день недели.
//
// Closes <= Opens означает работу через полночь: интервал продолжается
// в следующие сутки. Равенство — круглосуточная работа: [Opens, 24:00)
// плюс [00:00, Opens) закрывают сутки целиком.
type WorkInterval struct {
	Weekday Weekday
	Opens   TimeOfDay
	Closes  TimeOfDay
}

func (i WorkInterval) crossesMidnight() bool {
	return i.Closes <= i.Opens
}

// Schedule — расписание заведения вместе с его часовым поясом.
//
// Это чистая структура: она ничего не знает ни про базу, ни про HTTP.
// Расчёт «открыто сейчас» — классический источник багов (полночь, воскресенье,
// чужой часовой пояс), поэтому он живёт здесь и покрыт таблицей тестов.
type Schedule struct {
	Location  *time.Location
	Intervals []WorkInterval
}

// IsOpenAt отвечает, работает ли заведение в момент at.
//
// Момент приводится к часовому поясу заведения: заказ из Москвы во владивостокский
// ресторан должен считаться по владивостокскому времени, а не по времени клиента
// и не по UTC.
func (s Schedule) IsOpenAt(at time.Time) bool {
	if len(s.Intervals) == 0 {
		return false
	}

	local := at.In(s.location())
	today := WeekdayOf(local)
	now := TimeOfDayAt(local)

	for _, interval := range s.Intervals {
		// Интервал, начавшийся сегодня.
		if interval.Weekday == today {
			if interval.crossesMidnight() && now >= interval.Opens {
				return true
			}

			if !interval.crossesMidnight() && now >= interval.Opens && now < interval.Closes {
				return true
			}
		}

		// Хвост вчерашнего интервала, перешедшего через полночь.
		if interval.Weekday == today.prev() && interval.crossesMidnight() && now < interval.Closes {
			return true
		}
	}

	return false
}

// NextOpenAt возвращает ближайший момент открытия строго после at.
// Второе значение — false, если расписание пустое: тогда заведение
// не откроется никогда и обещать пользователю нечего.
//
// Заведение может быть открыто прямо сейчас — метод всё равно вернёт
// следующее открытие; вызывающий код спрашивает про него только тогда,
// когда IsOpenAt уже ответил «закрыто».
func (s Schedule) NextOpenAt(at time.Time) (time.Time, bool) {
	if len(s.Intervals) == 0 {
		return time.Time{}, false
	}

	local := at.In(s.location())

	// Неделя вперёд плюс сегодняшний день: расписание недельное, поэтому
	// если за восемь дней открытия не нашлось, его нет вовсе.
	for shift := range daysInWeek + 1 {
		day := local.AddDate(0, 0, shift)
		weekday := WeekdayOf(day)

		var best time.Time

		for _, interval := range s.Intervals {
			if interval.Weekday != weekday {
				continue
			}

			opensAt := atTimeOfDay(day, interval.Opens)
			if !opensAt.After(local) {
				continue
			}

			if best.IsZero() || opensAt.Before(best) {
				best = opensAt
			}
		}

		if !best.IsZero() {
			return best, true
		}
	}

	return time.Time{}, false
}

func (s Schedule) location() *time.Location {
	if s.Location == nil {
		return time.UTC
	}

	return s.Location
}

// atTimeOfDay собирает момент «этот день, это время суток» в зоне дня.
func atTimeOfDay(day time.Time, t TimeOfDay) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), int(t)/60, int(t)%60, 0, 0, day.Location())
}
