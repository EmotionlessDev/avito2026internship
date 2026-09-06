package catalog

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// Порядок выдачи — продуктовое решение, а не деталь SQL: открытые заведения
// выше закрытых, внутри группы — по запрошенному критерию. Тест держит
// это правило на месте.
func TestSortVenues(t *testing.T) {
	t.Parallel()

	distance := func(m int) *int { return &m }

	closedNear := VenueView{
		Venue:     Venue{ID: uuid.MustParse("01890000-0001-7000-8000-000000000001"), Name: "Ближнее закрытое"},
		IsOpenNow: false, DistanceM: distance(100),
	}
	openFar := VenueView{
		Venue:     Venue{ID: uuid.MustParse("01890000-0001-7000-8000-000000000002"), Name: "Яблоко"},
		IsOpenNow: true, DistanceM: distance(9000),
	}
	openNear := VenueView{
		Venue:     Venue{ID: uuid.MustParse("01890000-0001-7000-8000-000000000003"), Name: "Ёлка"},
		IsOpenNow: true, DistanceM: distance(500),
	}

	t.Run("по расстоянию: открытые выше, внутри — ближние выше", func(t *testing.T) {
		t.Parallel()

		views := []VenueView{closedNear, openFar, openNear}
		sortVenues(views, SortByDistance)

		assert.Equal(t, []string{"Ёлка", "Яблоко", "Ближнее закрытое"}, names(views))
	})

	t.Run("по названию: открытые выше, внутри — по алфавиту", func(t *testing.T) {
		t.Parallel()

		views := []VenueView{openFar, closedNear, openNear}
		sortVenues(views, SortByName)

		assert.Equal(t, []string{"Ёлка", "Яблоко", "Ближнее закрытое"}, names(views))
	})

	t.Run("без координат сортировка по расстоянию вырождается в алфавит", func(t *testing.T) {
		t.Parallel()

		views := []VenueView{
			{Venue: Venue{Name: "Супра"}, IsOpenNow: true},
			{Venue: Venue{Name: "Кухня"}, IsOpenNow: true},
		}
		sortVenues(views, SortByDistance)

		assert.Equal(t, []string{"Кухня", "Супра"}, names(views))
	})
}

// Пользовательский ввод не должен работать как шаблон LIKE: «%» ищет
// подстроку «%», а не «всё подряд».
func TestEscapeLike(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"хачапури":  "хачапури",
		"%":         `\%`,
		"_":         `\_`,
		`\`:         `\\`,
		"100% сыра": `100\% сыра`,
	}

	for in, want := range tests {
		assert.Equal(t, want, escapeLike(in))
	}
}

func names(views []VenueView) []string {
	out := make([]string, 0, len(views))
	for i := range views {
		out = append(out, views[i].Name)
	}

	return out
}
