package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/emotionlessdev/avito-kitchen/internal/events"
	"github.com/emotionlessdev/avito-kitchen/internal/kafkax"
	"github.com/emotionlessdev/avito-kitchen/internal/outbox"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Indexer наполняет витрину поиска из событий каталога.
//
// Витрина — проекция, а не источник истины: её можно потерять и пересобрать
// проигрыванием компактируемого топика. Поэтому индексатор ничего не читает
// из меню — всё, что ему нужно, приезжает в событии-снимке (docs/DESIGN.md §5).
//
// Это же выделенная точка замены: когда Postgres FTS перестанет хватать,
// сюда встанет запись в Elasticsearch, и остальной код не изменится.
type Indexer struct {
	pool     *pgxpool.Pool
	consumer string
	log      *slog.Logger
}

// NewIndexer собирает индексатор.
func NewIndexer(pool *pgxpool.Pool, log *slog.Logger) *Indexer {
	return &Indexer{
		pool:     pool,
		consumer: kafkax.GroupSearchIndexer,
		log:      log,
	}
}

// Handle применяет одно событие каталога.
func (i *Indexer) Handle(ctx context.Context, record kafkax.Record) error {
	var snapshot events.MenuItemSnapshot
	if err := json.Unmarshal(record.Value, &snapshot); err != nil {
		return fmt.Errorf("разбор события каталога: %w", err)
	}

	tx, err := i.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начало транзакции индексатора: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	fresh, err := outbox.MarkProcessed(ctx, tx, i.consumer, record.EventID)
	if err != nil {
		return err
	}

	if !fresh {
		// Событие уже применялось: повторная доставка — штатное дело.
		return nil
	}

	if err := i.apply(ctx, tx, snapshot); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("коммит индексатора: %w", err)
	}

	i.log.DebugContext(ctx, "витрина поиска обновлена",
		slog.String("item_id", snapshot.ItemID.String()),
		slog.Bool("archived", snapshot.IsArchived))

	return nil
}

func (i *Indexer) apply(ctx context.Context, tx pgx.Tx, snapshot events.MenuItemSnapshot) error {
	// Архивная позиция или заведение вне каталога — из выдачи убираем:
	// показывать то, чего нельзя заказать, хуже, чем не показывать вовсе.
	if snapshot.IsArchived || !snapshot.VenueIsActive {
		if _, err := tx.Exec(ctx,
			`DELETE FROM search_items WHERE item_id = $1`, snapshot.ItemID); err != nil {
			return fmt.Errorf("удаление позиции из витрины: %w", err)
		}

		return nil
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO search_items (item_id, venue_id, item_name, description, venue_name,
		                          category_name, price_minor, is_available, venue_is_active,
		                          venue_is_accepting, lat, lon, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		ON CONFLICT (item_id) DO UPDATE SET
			venue_id = EXCLUDED.venue_id,
			item_name = EXCLUDED.item_name,
			description = EXCLUDED.description,
			venue_name = EXCLUDED.venue_name,
			category_name = EXCLUDED.category_name,
			price_minor = EXCLUDED.price_minor,
			is_available = EXCLUDED.is_available,
			venue_is_active = EXCLUDED.venue_is_active,
			venue_is_accepting = EXCLUDED.venue_is_accepting,
			lat = EXCLUDED.lat,
			lon = EXCLUDED.lon,
			updated_at = now()`,
		snapshot.ItemID, snapshot.VenueID, snapshot.ItemName, snapshot.Description,
		snapshot.VenueName, snapshot.CategoryName, snapshot.PriceMinor, snapshot.IsAvailable,
		snapshot.VenueIsActive, snapshot.VenueIsAccepting, snapshot.Lat, snapshot.Lon)
	if err != nil {
		return fmt.Errorf("обновление витрины поиска: %w", err)
	}

	return nil
}
