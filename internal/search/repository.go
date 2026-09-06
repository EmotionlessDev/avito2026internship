package search

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository читает витрину поиска. Здесь остаётся только то, что умеет
// делать база: полнотекстовый разбор запроса и триграммное сходство.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository собирает репозиторий поиска.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// searchColumns — общий список колонок обеих выборок; порядок обязан
// совпадать с чтением в collect.
const searchColumns = `
	s.item_id, s.venue_id, s.item_name, s.description, s.venue_name,
	s.category_name, s.price_minor, s.is_available, s.venue_is_accepting,
	s.lat, s.lon`

// FullText ищет по полнотекстовому индексу с русским стеммингом.
//
// websearch_to_tsquery, а не plainto_tsquery: он понимает кавычки и минус
// в запросе, то есть ровно то, что пользователь привык печатать в строке поиска.
func (r *Repository) FullText(ctx context.Context, query string, limit int) ([]Item, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT`+searchColumns+`,
		       ts_rank(s.search_vector, websearch_to_tsquery('russian', $1)) AS relevance
		FROM search_items s
		WHERE s.venue_is_active
		  AND s.search_vector @@ websearch_to_tsquery('russian', $1)
		ORDER BY relevance DESC, s.item_name
		LIMIT $2`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("полнотекстовый поиск: %w", err)
	}

	return collect(rows)
}

// trigramThreshold — минимальное сходство, ниже которого добор бессмыслен:
// иначе на «хачапур» приедет половина меню.
const trigramThreshold = 0.25

// Trigram ищет по сходству названий — это добор для опечаток (сценарий U12).
//
// Триграммы не заменяют полнотекст, а дополняют его: стеммер не поможет
// с «хачапур» без последней буквы, а сходство строк — поможет.
func (r *Repository) Trigram(ctx context.Context, query string, limit int) ([]Item, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT`+searchColumns+`,
		       similarity(s.item_name, $1) AS relevance
		FROM search_items s
		WHERE s.venue_is_active
		  AND similarity(s.item_name, $1) >= $2
		ORDER BY relevance DESC, s.item_name
		LIMIT $3`, query, trigramThreshold, limit)
	if err != nil {
		return nil, fmt.Errorf("поиск по триграммам: %w", err)
	}

	return collect(rows)
}

func collect(rows pgx.Rows) ([]Item, error) {
	defer rows.Close()

	var items []Item

	for rows.Next() {
		var item Item

		if err := rows.Scan(
			&item.ItemID, &item.VenueID, &item.ItemName, &item.Description,
			&item.VenueName, &item.CategoryName, &item.PriceMinor,
			&item.IsAvailable, &item.VenueIsAccepting, &item.Lat, &item.Lon,
			&item.Relevance,
		); err != nil {
			return nil, fmt.Errorf("чтение выдачи поиска: %w", err)
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение выдачи поиска: %w", err)
	}

	return items, nil
}
