package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository читает каталог из Postgres. SQL написан руками (обоснование —
// PLAN.md, «Почему не ORM»): выборки каталога — это динамические фильтры
// и джойны, которые на ORM пишутся хуже, чем на SQL.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository собирает репозиторий каталога.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// venueColumns перечислен один раз: список колонок обязан совпадать
// с порядком чтения в scanVenue.
const venueColumns = `
	v.id, v.slug, v.name, v.address_text, v.lat, v.lon, v.timezone, v.status,
	v.is_accepting_orders, v.min_order_amount_minor, v.delivery_fee_minor,
	v.delivery_radius_m, v.avg_prep_minutes`

// ListVenues отдаёт опубликованные заведения, отфильтрованные по названию.
//
// Фильтры «открыто сейчас» и «рядом» здесь не применяются: они зависят от
// расписания и координат пользователя и считаются в сервисе — там же, где
// живут чистые функции и их тесты. Так правило существует в одном месте,
// а не в SQL и в Go одновременно.
func (r *Repository) ListVenues(ctx context.Context, nameQuery string) ([]Venue, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT`+venueColumns+`
		FROM venues v
		WHERE v.status = $1
		  AND ($2 = '' OR v.name ILIKE '%' || $2 || '%')
		ORDER BY v.name, v.id`, StatusActive, nameQuery)
	if err != nil {
		return nil, fmt.Errorf("выборка заведений: %w", err)
	}

	venues, err := collectVenues(rows)
	if err != nil {
		return nil, err
	}

	if err := r.attachSchedules(ctx, venues); err != nil {
		return nil, err
	}

	return venues, nil
}

// GetVenue отдаёт одно опубликованное заведение вместе с расписанием.
func (r *Repository) GetVenue(ctx context.Context, id uuid.UUID) (Venue, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT`+venueColumns+`
		FROM venues v
		WHERE v.id = $1 AND v.status = $2`, id, StatusActive)
	if err != nil {
		return Venue{}, fmt.Errorf("выборка заведения: %w", err)
	}

	venues, err := collectVenues(rows)
	if err != nil {
		return Venue{}, err
	}

	if len(venues) == 0 {
		return Venue{}, ErrVenueNotFound
	}

	if err := r.attachSchedules(ctx, venues); err != nil {
		return Venue{}, err
	}

	return venues[0], nil
}

// SchedulesByVenueIDs отдаёт расписания перечисленных заведений.
// Нужен поиску: витрина search_items расписание не хранит, а признак
// «заведение сейчас открыто» в выдаче показать надо.
func (r *Repository) SchedulesByVenueIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Schedule, error) {
	schedules := make(map[uuid.UUID]Schedule, len(ids))
	if len(ids) == 0 {
		return schedules, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT v.id, v.timezone,
		       to_char(wh.opens_at, 'HH24:MI'), to_char(wh.closes_at, 'HH24:MI'), wh.weekday
		FROM venues v
		JOIN venue_work_hours wh ON wh.venue_id = v.id
		WHERE v.id = ANY($1)
		ORDER BY v.id, wh.weekday, wh.opens_at`, ids)
	if err != nil {
		return nil, fmt.Errorf("выборка расписаний: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var (
			venueID       uuid.UUID
			timezone      string
			opens, closes string
			weekday       int
		)

		if err := rows.Scan(&venueID, &timezone, &opens, &closes, &weekday); err != nil {
			return nil, fmt.Errorf("чтение расписания: %w", err)
		}

		interval, err := parseInterval(weekday, opens, closes)
		if err != nil {
			return nil, err
		}

		schedule, ok := schedules[venueID]
		if !ok {
			location, err := loadLocation(timezone)
			if err != nil {
				return nil, err
			}

			schedule = Schedule{Location: location}
		}

		schedule.Intervals = append(schedule.Intervals, interval)
		schedules[venueID] = schedule
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение расписаний: %w", err)
	}

	return schedules, nil
}

// GetMenu собирает меню заведения: категории, позиции и их модификаторы.
//
// includeUnavailable оставляет в выдаче позиции из стоп-листа — витрине
// полезно показать «сегодня закончилось», а не прятать блюдо совсем.
func (r *Repository) GetMenu(ctx context.Context, venueID uuid.UUID, includeUnavailable bool) (Menu, error) {
	if _, err := r.GetVenue(ctx, venueID); err != nil {
		return Menu{}, err
	}

	categories, refs, err := r.menuCategories(ctx, venueID, includeUnavailable)
	if err != nil {
		return Menu{}, err
	}

	if err := r.attachOptionGroups(ctx, categories, refs); err != nil {
		return Menu{}, err
	}

	return Menu{VenueID: venueID, Categories: categories}, nil
}

// menuCategories читает категории вместе с позициями одним запросом.
// Категории без единой видимой позиции в выдачу не попадают: пустой раздел
// меню — это шум для клиента.
func (r *Repository) menuCategories(
	ctx context.Context, venueID uuid.UUID, includeUnavailable bool,
) ([]Category, map[uuid.UUID]itemRef, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.external_id, c.name, c.position,
		       i.id, i.external_id, i.name, i.description, i.price_minor,
		       i.weight_grams, i.position, i.is_available
		FROM menu_categories c
		JOIN menu_items i ON i.category_id = c.id
		WHERE c.venue_id = $1
		  AND NOT i.is_archived
		  AND ($2 OR i.is_available)
		ORDER BY c.position, c.id, i.position, i.id`, venueID, includeUnavailable)
	if err != nil {
		return nil, nil, fmt.Errorf("выборка меню: %w", err)
	}

	defer rows.Close()

	var categories []Category

	refs := make(map[uuid.UUID]itemRef)
	indexByCategory := make(map[uuid.UUID]int)

	for rows.Next() {
		var (
			category Category
			item     Item
		)

		if err := rows.Scan(
			&category.ID, &category.ExternalID, &category.Name, &category.Position,
			&item.ID, &item.ExternalID, &item.Name, &item.Description, &item.PriceMinor,
			&item.WeightGrams, &item.Position, &item.IsAvailable,
		); err != nil {
			return nil, nil, fmt.Errorf("чтение меню: %w", err)
		}

		index, ok := indexByCategory[category.ID]
		if !ok {
			categories = append(categories, category)
			index = len(categories) - 1
			indexByCategory[category.ID] = index
		}

		categories[index].Items = append(categories[index].Items, item)
		refs[item.ID] = itemRef{category: index, item: len(categories[index].Items) - 1}
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("чтение меню: %w", err)
	}

	return categories, refs, nil
}

// itemRef — место позиции в собранной выдаче. Индексы, а не указатели:
// срез позиций дописывается по ходу чтения строк, и указатель на его элемент
// протух бы при первом же перевыделении массива.
type itemRef struct {
	category int
	item     int
}

// attachOptionGroups дочитывает модификаторы к уже собранным позициям.
// Отдельный запрос, а не джойн к меню: иначе каждая позиция размножилась бы
// на число своих опций, и разбирать это на клиенте пришлось бы всё равно.
func (r *Repository) attachOptionGroups(
	ctx context.Context, categories []Category, refs map[uuid.UUID]itemRef,
) error {
	if len(refs) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT g.item_id, g.id, g.external_id, g.name, g.min_choices, g.max_choices, g.position,
		       o.id, o.external_id, o.name, o.price_delta_minor, o.position, o.is_available
		FROM menu_option_groups g
		JOIN menu_options o ON o.group_id = g.id
		WHERE g.item_id = ANY($1)
		ORDER BY g.item_id, g.position, g.id, o.position, o.id`, ids)
	if err != nil {
		return fmt.Errorf("выборка модификаторов: %w", err)
	}

	defer rows.Close()

	groupIndex := make(map[uuid.UUID]int)

	for rows.Next() {
		var (
			itemID uuid.UUID
			group  OptionGroup
			option Option
		)

		if err := rows.Scan(
			&itemID, &group.ID, &group.ExternalID, &group.Name,
			&group.MinChoices, &group.MaxChoices, &group.Position,
			&option.ID, &option.ExternalID, &option.Name,
			&option.PriceDeltaMinor, &option.Position, &option.IsAvailable,
		); err != nil {
			return fmt.Errorf("чтение модификаторов: %w", err)
		}

		ref, ok := refs[itemID]
		if !ok {
			continue
		}

		item := &categories[ref.category].Items[ref.item]

		index, ok := groupIndex[group.ID]
		if !ok {
			item.OptionGroups = append(item.OptionGroups, group)
			index = len(item.OptionGroups) - 1
			groupIndex[group.ID] = index
		}

		item.OptionGroups[index].Options = append(item.OptionGroups[index].Options, option)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("чтение модификаторов: %w", err)
	}

	return nil
}

func (r *Repository) attachSchedules(ctx context.Context, venues []Venue) error {
	if len(venues) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, 0, len(venues))
	for i := range venues {
		ids = append(ids, venues[i].ID)
	}

	schedules, err := r.SchedulesByVenueIDs(ctx, ids)
	if err != nil {
		return err
	}

	for i := range venues {
		schedule, ok := schedules[venues[i].ID]
		if !ok {
			// Расписания нет — заведение всегда закрыто. Это честнее, чем
			// считать его круглосуточным: пустое расписание означает, что
			// заведение его ещё не завело.
			location, err := loadLocation(venues[i].Timezone)
			if err != nil {
				return err
			}

			schedule = Schedule{Location: location}
		}

		venues[i].Schedule = schedule
	}

	return nil
}

func collectVenues(rows pgx.Rows) ([]Venue, error) {
	defer rows.Close()

	var venues []Venue

	for rows.Next() {
		var venue Venue

		if err := rows.Scan(
			&venue.ID, &venue.Slug, &venue.Name, &venue.AddressText,
			&venue.Point.Lat, &venue.Point.Lon, &venue.Timezone, &venue.Status,
			&venue.IsAcceptingOrders, &venue.MinOrderAmountMinor, &venue.DeliveryFeeMinor,
			&venue.DeliveryRadiusM, &venue.AvgPrepMinutes,
		); err != nil {
			return nil, fmt.Errorf("чтение заведения: %w", err)
		}

		venues = append(venues, venue)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение заведений: %w", err)
	}

	return venues, nil
}

func parseInterval(weekday int, opens, closes string) (WorkInterval, error) {
	opensAt, err := ParseTimeOfDay(opens)
	if err != nil {
		return WorkInterval{}, fmt.Errorf("расписание: %w", err)
	}

	closesAt, err := ParseTimeOfDay(closes)
	if err != nil {
		return WorkInterval{}, fmt.Errorf("расписание: %w", err)
	}

	return WorkInterval{Weekday: Weekday(weekday), Opens: opensAt, Closes: closesAt}, nil
}

// loadLocation переводит IANA-имя зоны в *time.Location. Ошибка означает,
// что в образе нет tzdata или в базе лежит опечатка: и то и другое —
// повод упасть, а не молча считать расписание по UTC.
func loadLocation(timezone string) (*time.Location, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", errUnknownTimezone, timezone)
	}

	return location, nil
}

var errUnknownTimezone = errors.New("неизвестный часовой пояс заведения")
