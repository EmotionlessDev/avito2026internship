// Package search — сквозной поиск блюд по всем заведениям.
//
// Пользователь ищет блюдо, а не заведение (docs/DESIGN.md §9), поэтому выдача
// собирается из денормализованной витрины search_items: позиция там уже склеена
// с заведением, категорией и признаком доступности.
//
// Полнотекст остаётся в Postgres — это работа, которую умеет только СУБД.
// Ранжирование, наоборот, живёт здесь: в него входят доступность и близость,
// которых нет в индексе, и правило должно быть одно и проверяемое.
package search

import (
	"github.com/google/uuid"
)

// Item — найденное блюдо вместе с заведением, которое его готовит.
type Item struct {
	ItemID           uuid.UUID
	VenueID          uuid.UUID
	ItemName         string
	Description      string
	VenueName        string
	CategoryName     string
	PriceMinor       int64
	IsAvailable      bool
	VenueIsAccepting bool
	Lat              float64
	Lon              float64

	// Relevance — оценка текстового совпадения из Postgres: ts_rank для
	// полнотекста или similarity для триграммного добора.
	Relevance float64
}

// Result — блюдо, готовое к показу: с расстоянием, признаком открытости
// заведения и итоговой оценкой, по которой построен порядок выдачи.
type Result struct {
	Item

	VenueIsOpenNow bool
	DistanceM      *int
	Score          float64
}

// Коэффициенты ранжирования. Вынесены в константы, чтобы правило читалось
// целиком в одном месте, а не собиралось по коду.
const (
	// unavailablePenalty опускает позицию из стоп-листа вниз, но не выбрасывает:
	// пользователю полезно знать, что блюдо здесь бывает (docs/DESIGN.md §9).
	unavailablePenalty = 0.2
	// pausedVenuePenalty — заведение на паузе принимает заказы не сейчас,
	// это мягче, чем закончившееся блюдо.
	pausedVenuePenalty = 0.5
	// closedVenuePenalty — заведение закрыто по расписанию.
	closedVenuePenalty = 0.4
	// proximityHalfLifeKm — расстояние, на котором вес позиции падает вдвое.
	proximityHalfLifeKm = 5.0
)

// Score считает итоговый вес позиции в выдаче: текстовая релевантность,
// умноженная на доступность и близость.
//
// Умножение, а не сложение: каждая причина «это блюдо сейчас хуже» должна
// работать независимо от масштаба ts_rank, который у разных запросов разный.
func Score(relevance float64, isAvailable, venueOpen, venueAccepting bool, distanceM *int) float64 {
	score := relevance

	if !isAvailable {
		score *= unavailablePenalty
	}

	if !venueOpen {
		score *= closedVenuePenalty
	}

	if !venueAccepting {
		score *= pausedVenuePenalty
	}

	if distanceM != nil {
		km := float64(*distanceM) / 1000
		score *= proximityHalfLifeKm / (proximityHalfLifeKm + km)
	}

	return score
}
