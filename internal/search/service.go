package search

import (
	"context"
	"sort"
	"strings"

	"github.com/emotionlessdev/avito-kitchen/internal/catalog"
	"github.com/emotionlessdev/avito-kitchen/internal/paging"

	"github.com/google/uuid"
)

// VenueOpenState отвечает, какие заведения открыты прямо сейчас.
//
// Интерфейс объявлен здесь, у потребителя: поиску нужен ровно этот вопрос,
// а реализует его сервис каталога, где живёт расписание.
type VenueOpenState interface {
	OpenNowByVenueIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error)
}

// Query — поисковый запрос пользователя.
type Query struct {
	Text          string
	Point         *catalog.Point
	OnlyAvailable bool
	Cursor        string
	Limit         int
}

// Page — страница выдачи поиска.
type Page struct {
	Items      []Result
	NextCursor string
}

// Index — то, что сервису нужно от витрины: два способа найти совпадения.
// Интерфейс у потребителя, реализация — Repository; ранжирование и добор
// проверяются тестами без Postgres.
type Index interface {
	FullText(ctx context.Context, query string, limit int) ([]Item, error)
	Trigram(ctx context.Context, query string, limit int) ([]Item, error)
}

// Service собирает выдачу: спрашивает у базы совпадения, добавляет то,
// чего в индексе нет, и раскладывает результат по весу.
type Service struct {
	repo   Index
	venues VenueOpenState
}

// NewService собирает сервис поиска.
func NewService(repo Index, venues VenueOpenState) *Service {
	return &Service{repo: repo, venues: venues}
}

const (
	// candidateFactor — во сколько раз больше страницы забираем из базы:
	// ранжирование по близости и доступности переставляет выдачу, поэтому
	// брать ровно limit нельзя — верхние строки после пересортировки уедут.
	candidateFactor = 3
	// minCandidates — нижняя граница выборки, чтобы на первой странице
	// пересортировке было из чего выбирать.
	minCandidates = 50
	// trigramFallbackThreshold — если полнотекст нашёл меньше, запрос похож
	// на опечатку, и его стоит добрать триграммами.
	trigramFallbackThreshold = 5
)

// Search ищет блюда по всем заведениям.
func (s *Service) Search(ctx context.Context, query Query) (Page, error) {
	text := strings.TrimSpace(query.Text)

	offset, err := paging.DecodeOffset(query.Cursor)
	if err != nil {
		return Page{}, err
	}

	candidates := max(minCandidates, (offset+query.Limit)*candidateFactor)

	items, err := s.repo.FullText(ctx, text, candidates)
	if err != nil {
		return Page{}, err
	}

	// Полнотекст почти ничего не нашёл — вероятно, опечатка (сценарий U12).
	if len(items) < trigramFallbackThreshold {
		fuzzy, fuzzyErr := s.repo.Trigram(ctx, text, candidates)
		if fuzzyErr != nil {
			return Page{}, fuzzyErr
		}

		items = merge(items, fuzzy)
	}

	if query.OnlyAvailable {
		items = keepAvailable(items)
	}

	openState, err := s.venues.OpenNowByVenueIDs(ctx, venueIDs(items))
	if err != nil {
		return Page{}, err
	}

	results := s.rank(items, openState, query.Point)

	page, next := paging.Page(results, offset, query.Limit)

	return Page{Items: page, NextCursor: next}, nil
}

func (s *Service) rank(items []Item, openState map[uuid.UUID]bool, point *catalog.Point) []Result {
	results := make([]Result, 0, len(items))

	for i := range items {
		item := &items[i]
		result := Result{Item: *item, VenueIsOpenNow: openState[item.VenueID]}

		if point != nil {
			distance := catalog.DistanceM(*point, catalog.Point{Lat: item.Lat, Lon: item.Lon})
			result.DistanceM = &distance
		}

		result.Score = Score(
			item.Relevance, item.IsAvailable, result.VenueIsOpenNow,
			item.VenueIsAccepting, result.DistanceM,
		)

		results = append(results, result)
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}

		return results[i].ItemName < results[j].ItemName
	})

	return results
}

// merge добавляет к полнотекстовой выдаче триграммный добор, не задваивая
// позиции: одно и то же блюдо может найтись обоими способами.
func merge(primary, extra []Item) []Item {
	seen := make(map[uuid.UUID]struct{}, len(primary))
	for i := range primary {
		seen[primary[i].ItemID] = struct{}{}
	}

	for i := range extra {
		if _, ok := seen[extra[i].ItemID]; ok {
			continue
		}

		primary = append(primary, extra[i])
		seen[extra[i].ItemID] = struct{}{}
	}

	return primary
}

func keepAvailable(items []Item) []Item {
	kept := make([]Item, 0, len(items))

	for i := range items {
		if items[i].IsAvailable {
			kept = append(kept, items[i])
		}
	}

	return kept
}

func venueIDs(items []Item) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(items))
	ids := make([]uuid.UUID, 0, len(items))

	for i := range items {
		if _, ok := seen[items[i].VenueID]; ok {
			continue
		}

		seen[items[i].VenueID] = struct{}{}
		ids = append(ids, items[i].VenueID)
	}

	return ids
}
