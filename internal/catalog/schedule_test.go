package catalog_test

import (
	"testing"
	"time"

	"github.com/emotionlessdev/avito-kitchen/internal/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustLoad — часовой пояс обязан существовать: без tzdata расчёт расписания
// бессмыслен, и падение теста здесь лучше, чем тихое «всё закрыто» в проде.
func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()

	loc, err := time.LoadLocation(name)
	require.NoError(t, err)

	return loc
}

func hhmm(t *testing.T, s string) catalog.TimeOfDay {
	t.Helper()

	v, err := catalog.ParseTimeOfDay(s)
	require.NoError(t, err)

	return v
}

// Расписание «Супры» из сида: будни до 23:00, пятница и суббота — через
// полночь, воскресенье короче. Именно на нём ломаются наивные реализации.
func supraSchedule(t *testing.T) catalog.Schedule {
	t.Helper()

	moscow := mustLoad(t, "Europe/Moscow")

	return catalog.Schedule{
		Location: moscow,
		Intervals: []catalog.WorkInterval{
			{Weekday: catalog.Monday, Opens: hhmm(t, "12:00"), Closes: hhmm(t, "23:00")},
			{Weekday: catalog.Tuesday, Opens: hhmm(t, "12:00"), Closes: hhmm(t, "23:00")},
			{Weekday: catalog.Wednesday, Opens: hhmm(t, "12:00"), Closes: hhmm(t, "23:00")},
			{Weekday: catalog.Thursday, Opens: hhmm(t, "12:00"), Closes: hhmm(t, "23:00")},
			{Weekday: catalog.Friday, Opens: hhmm(t, "12:00"), Closes: hhmm(t, "02:00")},
			{Weekday: catalog.Saturday, Opens: hhmm(t, "12:00"), Closes: hhmm(t, "02:00")},
			{Weekday: catalog.Sunday, Opens: hhmm(t, "12:00"), Closes: hhmm(t, "22:00")},
		},
	}
}

func TestScheduleIsOpenAt(t *testing.T) {
	t.Parallel()

	moscow := mustLoad(t, "Europe/Moscow")
	vladivostok := mustLoad(t, "Asia/Vladivostok")
	schedule := supraSchedule(t)

	// 2026-09-04 — пятница, 05 — суббота, 06 — воскресенье, 07 — понедельник.
	tests := map[string]struct {
		at   time.Time
		want bool
	}{
		"будний день внутри интервала": {
			at:   time.Date(2026, 9, 7, 13, 0, 0, 0, moscow),
			want: true,
		},
		"будний день до открытия": {
			at:   time.Date(2026, 9, 7, 11, 59, 0, 0, moscow),
			want: false,
		},
		"будний день ровно в момент открытия": {
			at:   time.Date(2026, 9, 7, 12, 0, 0, 0, moscow),
			want: true,
		},
		"будний день ровно в момент закрытия — уже закрыто": {
			at:   time.Date(2026, 9, 7, 23, 0, 0, 0, moscow),
			want: false,
		},
		"ночь субботы — хвост пятничного интервала": {
			at:   time.Date(2026, 9, 5, 1, 30, 0, 0, moscow),
			want: true,
		},
		"ночь субботы после закрытия пятничного интервала": {
			at:   time.Date(2026, 9, 5, 2, 0, 0, 0, moscow),
			want: false,
		},
		"утро субботы — уже закрыто, ещё не открылось": {
			at:   time.Date(2026, 9, 5, 9, 0, 0, 0, moscow),
			want: false,
		},
		"ночь воскресенья — хвост субботнего интервала": {
			at:   time.Date(2026, 9, 6, 0, 30, 0, 0, moscow),
			want: true,
		},
		"воскресенье закрывается раньше буднего дня": {
			at:   time.Date(2026, 9, 6, 22, 30, 0, 0, moscow),
			want: false,
		},
		"ночь понедельника — воскресный интервал через полночь не идёт": {
			at:   time.Date(2026, 9, 7, 0, 30, 0, 0, moscow),
			want: false,
		},
		"момент задан в чужом поясе — считается по поясу заведения": {
			// 20:00 во Владивостоке — это 13:00 в Москве, заведение открыто.
			at:   time.Date(2026, 9, 7, 20, 0, 0, 0, vladivostok),
			want: true,
		},
		"момент задан в UTC — тоже приводится к поясу заведения": {
			// 22:30 UTC понедельника — 01:30 вторника в Москве, уже закрыто.
			at:   time.Date(2026, 9, 7, 22, 30, 0, 0, time.UTC),
			want: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, schedule.IsOpenAt(tt.at))
		})
	}
}

// Заведение в другом часовом поясе: то же расписание «с 12:00», но открытие
// наступает на семь часов раньше по московским часам.
func TestScheduleIsOpenAtRespectsVenueTimezone(t *testing.T) {
	t.Parallel()

	moscow := mustLoad(t, "Europe/Moscow")
	schedule := catalog.Schedule{
		Location: mustLoad(t, "Asia/Vladivostok"),
		Intervals: []catalog.WorkInterval{
			{Weekday: catalog.Monday, Opens: hhmm(t, "12:00"), Closes: hhmm(t, "23:00")},
		},
	}

	// 06:00 московского понедельника — 13:00 владивостокского: открыто.
	assert.True(t, schedule.IsOpenAt(time.Date(2026, 9, 7, 6, 0, 0, 0, moscow)))
	// 17:00 московского понедельника — уже полночь вторника во Владивостоке.
	assert.False(t, schedule.IsOpenAt(time.Date(2026, 9, 7, 17, 0, 0, 0, moscow)))
}

func TestScheduleIsOpenAtEdgeCases(t *testing.T) {
	t.Parallel()

	moscow := mustLoad(t, "Europe/Moscow")

	t.Run("пустое расписание — всегда закрыто", func(t *testing.T) {
		t.Parallel()

		var schedule catalog.Schedule

		assert.False(t, schedule.IsOpenAt(time.Date(2026, 9, 7, 13, 0, 0, 0, moscow)))
	})

	t.Run("равные границы — круглосуточно", func(t *testing.T) {
		t.Parallel()

		schedule := catalog.Schedule{
			Location: moscow,
			Intervals: []catalog.WorkInterval{
				{Weekday: catalog.Monday, Opens: hhmm(t, "00:00"), Closes: hhmm(t, "00:00")},
				{Weekday: catalog.Tuesday, Opens: hhmm(t, "00:00"), Closes: hhmm(t, "00:00")},
			},
		}

		assert.True(t, schedule.IsOpenAt(time.Date(2026, 9, 7, 3, 0, 0, 0, moscow)))
		assert.True(t, schedule.IsOpenAt(time.Date(2026, 9, 7, 23, 59, 0, 0, moscow)))
	})
}

func TestScheduleNextOpenAt(t *testing.T) {
	t.Parallel()

	moscow := mustLoad(t, "Europe/Moscow")
	schedule := supraSchedule(t)

	tests := map[string]struct {
		at   time.Time
		want time.Time
	}{
		"утро буднего дня — откроется сегодня в 12:00": {
			at:   time.Date(2026, 9, 7, 9, 0, 0, 0, moscow),
			want: time.Date(2026, 9, 7, 12, 0, 0, 0, moscow),
		},
		"поздний вечер — откроется завтра": {
			at:   time.Date(2026, 9, 7, 23, 30, 0, 0, moscow),
			want: time.Date(2026, 9, 8, 12, 0, 0, 0, moscow),
		},
		"ночь субботы после закрытия — откроется в субботу днём": {
			at:   time.Date(2026, 9, 5, 2, 30, 0, 0, moscow),
			want: time.Date(2026, 9, 5, 12, 0, 0, 0, moscow),
		},
		"вопрос из чужого пояса — ответ в поясе заведения": {
			at:   time.Date(2026, 9, 7, 16, 0, 0, 0, mustLoad(t, "Asia/Vladivostok")),
			want: time.Date(2026, 9, 7, 12, 0, 0, 0, moscow),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := schedule.NextOpenAt(tt.at)
			require.True(t, ok)
			assert.True(t, tt.want.Equal(got), "ожидалось %s, получено %s", tt.want, got)
		})
	}
}

func TestScheduleNextOpenAtEmpty(t *testing.T) {
	t.Parallel()

	var schedule catalog.Schedule

	_, ok := schedule.NextOpenAt(time.Now())
	assert.False(t, ok, "у пустого расписания нет момента открытия")
}

func TestParseTimeOfDay(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in      string
		want    catalog.TimeOfDay
		wantErr bool
	}{
		"часы и минуты":       {in: "10:30", want: 630},
		"формат колонки time": {in: "23:00:00", want: 1380},
		"полночь":             {in: "00:00", want: 0},
		"без двоеточия":       {in: "1030", wantErr: true},
		"часы вне суток":      {in: "24:00", wantErr: true},
		"минуты вне часа":     {in: "10:60", wantErr: true},
		"мусор вместо чисел":  {in: "ab:cd", wantErr: true},
		"лишние составляющие": {in: "10:00:00:00", wantErr: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := catalog.ParseTimeOfDay(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, catalog.ErrBadTimeOfDay)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.in[:5], got.String())
		})
	}
}
