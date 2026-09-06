package merchant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository — хранилище контура заведений.
//
// Каждый метод, работающий с данными заведения, сам достаёт venue_id
// из контекста и подставляет его в WHERE. Так изоляция арендаторов держится
// на репозитории, а не на дисциплине вызывающего кода: хендлеру просто нечего
// передать, чтобы прочитать чужое.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository собирает репозиторий контура заведений.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// AuthenticateKey находит заведение по отпечатку ключа.
//
// Поиск идёт по key_hash, у которого уникальный индекс: это один
// индексный доступ и никакого перебора кандидатов по префиксу.
func (r *Repository) AuthenticateKey(ctx context.Context, keyHash string) (uuid.UUID, error) {
	var venueID uuid.UUID

	err := r.pool.QueryRow(ctx, `
		SELECT k.venue_id
		FROM venue_api_keys k
		JOIN venues v ON v.id = k.venue_id
		WHERE k.key_hash = $1
		  AND k.revoked_at IS NULL
		  AND v.status = 'active'`, keyHash).Scan(&venueID)

	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrUnauthorized
	}

	if err != nil {
		return uuid.Nil, fmt.Errorf("проверка ключа: %w", err)
	}

	return venueID, nil
}

// CurrentMenu собирает текущее состояние меню заведения для сведения
// со снапшотом. Архивированные позиции читаются тоже: вернувшийся артикул
// должен обновить старую запись, а не создать дубль.
func (r *Repository) CurrentMenu(ctx context.Context) (CurrentMenu, error) {
	venueID, err := TenantFrom(ctx)
	if err != nil {
		return CurrentMenu{}, err
	}

	menu := CurrentMenu{
		Categories: map[string]CurrentCategory{},
		Items:      map[string]CurrentItem{},
	}

	if readErr := r.readCategories(ctx, venueID, menu); readErr != nil {
		return CurrentMenu{}, readErr
	}

	itemsByID, err := r.readItems(ctx, venueID, menu)
	if err != nil {
		return CurrentMenu{}, err
	}

	if readErr := r.readOptionGroups(ctx, venueID, menu, itemsByID); readErr != nil {
		return CurrentMenu{}, readErr
	}

	return menu, nil
}

func (r *Repository) readCategories(ctx context.Context, venueID uuid.UUID, menu CurrentMenu) error {
	rows, err := r.pool.Query(ctx, `
		SELECT id, external_id, name, position
		FROM menu_categories
		WHERE venue_id = $1`, venueID)
	if err != nil {
		return fmt.Errorf("текущие категории: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var (
			externalID string
			category   CurrentCategory
		)

		if err := rows.Scan(&category.ID, &externalID, &category.Name, &category.Position); err != nil {
			return fmt.Errorf("чтение категории: %w", err)
		}

		menu.Categories[externalID] = category
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("чтение категорий: %w", err)
	}

	return nil
}

func (r *Repository) readItems(
	ctx context.Context, venueID uuid.UUID, menu CurrentMenu,
) (map[uuid.UUID]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT i.id, i.external_id, c.external_id, i.name, i.description, i.price_minor,
		       i.weight_grams, i.position, i.is_available, i.is_archived
		FROM menu_items i
		JOIN menu_categories c ON c.id = i.category_id
		WHERE i.venue_id = $1`, venueID)
	if err != nil {
		return nil, fmt.Errorf("текущие позиции: %w", err)
	}

	defer rows.Close()

	itemsByID := make(map[uuid.UUID]string)

	for rows.Next() {
		var (
			externalID string
			item       CurrentItem
		)

		if err := rows.Scan(
			&item.ID, &externalID, &item.CategoryExternalID, &item.Name, &item.Description,
			&item.PriceMinor, &item.WeightGrams, &item.Position, &item.IsAvailable, &item.IsArchived,
		); err != nil {
			return nil, fmt.Errorf("чтение позиции: %w", err)
		}

		item.OptionGroups = map[string]CurrentOptionGroup{}
		menu.Items[externalID] = item
		itemsByID[item.ID] = externalID
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение позиций: %w", err)
	}

	return itemsByID, nil
}

func (r *Repository) readOptionGroups(
	ctx context.Context, venueID uuid.UUID, menu CurrentMenu, itemsByID map[uuid.UUID]string,
) error {
	if len(itemsByID) == 0 {
		return nil
	}

	// LEFT JOIN, потому что группа без опций — валидное состояние: заведение
	// могло создать «Добавки» и пока не завести ни одной.
	rows, err := r.pool.Query(ctx, `
		SELECT g.item_id, g.id, g.external_id, g.name, g.min_choices, g.max_choices, g.position,
		       o.id, o.external_id, o.name, o.price_delta_minor, o.position, o.is_available
		FROM menu_option_groups g
		JOIN menu_items i ON i.id = g.item_id
		LEFT JOIN menu_options o ON o.group_id = g.id
		WHERE i.venue_id = $1`, venueID)
	if err != nil {
		return fmt.Errorf("текущие модификаторы: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var (
			itemID        uuid.UUID
			groupExternal string
			group         CurrentOptionGroup
			option        nullableOption
		)

		if err := rows.Scan(
			&itemID, &group.ID, &groupExternal, &group.Name,
			&group.MinChoices, &group.MaxChoices, &group.Position,
			&option.ID, &option.ExternalID, &option.Name,
			&option.PriceDeltaMinor, &option.Position, &option.IsAvailable,
		); err != nil {
			return fmt.Errorf("чтение модификатора: %w", err)
		}

		itemExternal, known := itemsByID[itemID]
		if !known {
			continue
		}

		item := menu.Items[itemExternal]

		existing, found := item.OptionGroups[groupExternal]
		if !found {
			group.Options = map[string]CurrentOption{}
			existing = group
		}

		if option.filled() {
			existing.Options[*option.ExternalID] = option.value()
		}

		item.OptionGroups[groupExternal] = existing
		menu.Items[itemExternal] = item
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("чтение модификаторов: %w", err)
	}

	return nil
}

// nullableOption — правая часть LEFT JOIN: у группы без опций все её колонки
// приходят пустыми, и разбирать это указателями честнее, чем нулевыми значениями.
type nullableOption struct {
	ID              *uuid.UUID
	ExternalID      *string
	Name            *string
	PriceDeltaMinor *int64
	Position        *int
	IsAvailable     *bool
}

func (o nullableOption) filled() bool {
	return o.ID != nil && o.ExternalID != nil
}

func (o nullableOption) value() CurrentOption {
	option := CurrentOption{ID: *o.ID}

	if o.Name != nil {
		option.Name = *o.Name
	}

	if o.PriceDeltaMinor != nil {
		option.PriceDeltaMinor = *o.PriceDeltaMinor
	}

	if o.Position != nil {
		option.Position = *o.Position
	}

	if o.IsAvailable != nil {
		option.IsAvailable = *o.IsAvailable
	}

	return option
}

// LastImportHash отдаёт отпечаток последнего импорта заведения.
//
// Идемпотентность считается по последнему импорту, а не по всей истории:
// заведение имеет право откатить меню к предыдущему снапшоту, и такой импорт
// обязан примениться, а не отсеяться как «уже видели».
func (r *Repository) LastImportHash(ctx context.Context) (string, error) {
	venueID, err := TenantFrom(ctx)
	if err != nil {
		return "", err
	}

	var hash string

	err = r.pool.QueryRow(ctx, `
		SELECT payload_hash
		FROM menu_imports
		WHERE venue_id = $1 AND finished_at IS NOT NULL
		ORDER BY started_at DESC
		LIMIT 1`, venueID).Scan(&hash)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("последний импорт: %w", err)
	}

	return hash, nil
}

// ApplyImport записывает план изменений и аудит одной транзакцией.
//
// Одна транзакция здесь обязательна: меню, витрина поиска и запись аудита
// должны стать видимыми вместе. Иначе поиск успеет показать позиции,
// которых в меню уже нет, а счётчики импорта не совпадут с данными.
func (r *Repository) ApplyImport(
	ctx context.Context, importID uuid.UUID, payloadHash string, changes Changes,
) error {
	venueID, err := TenantFrom(ctx)
	if err != nil {
		return err
	}

	return r.inTx(ctx, func(tx pgx.Tx) error {
		if err := applyCategories(ctx, tx, venueID, changes.Categories); err != nil {
			return err
		}

		if err := applyItems(ctx, tx, venueID, changes.Items); err != nil {
			return err
		}

		if err := archiveItems(ctx, tx, venueID, changes.ArchiveIDs); err != nil {
			return err
		}

		if err := writeImportAudit(ctx, tx, venueID, importID, payloadHash, changes); err != nil {
			return err
		}

		// Пустой план — только запись в аудит. Витрину не трогаем: иначе
		// каждый повторный импорт переписывал бы все строки поиска ради
		// одинакового результата. Расхождения витрины с меню чинит
		// не импорт, а суточная сверка (docs/DESIGN.md §6.4).
		if changes.IsEmpty() {
			return nil
		}

		return refreshSearchItems(ctx, tx, venueID)
	})
}

func applyCategories(ctx context.Context, tx pgx.Tx, venueID uuid.UUID, categories []CategoryChange) error {
	for _, category := range categories {
		_, err := tx.Exec(ctx, `
			INSERT INTO menu_categories (id, venue_id, external_id, name, position)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (venue_id, external_id) DO UPDATE
			SET name = EXCLUDED.name, position = EXCLUDED.position, updated_at = now()`,
			category.ID, venueID, category.ExternalID, category.Name, category.Position)
		if err != nil {
			return fmt.Errorf("категория %q: %w", category.ExternalID, err)
		}
	}

	return nil
}

func applyItems(ctx context.Context, tx pgx.Tx, venueID uuid.UUID, items []ItemChange) error {
	for i := range items {
		item := items[i]

		_, err := tx.Exec(ctx, `
			INSERT INTO menu_items (id, venue_id, category_id, external_id, name, description,
			                        price_minor, weight_grams, position, is_available, is_archived)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, false)
			ON CONFLICT (venue_id, external_id) DO UPDATE
			SET category_id = EXCLUDED.category_id,
			    name = EXCLUDED.name,
			    description = EXCLUDED.description,
			    price_minor = EXCLUDED.price_minor,
			    weight_grams = EXCLUDED.weight_grams,
			    position = EXCLUDED.position,
			    is_available = EXCLUDED.is_available,
			    is_archived = false,
			    updated_at = now()`,
			item.ID, venueID, item.CategoryID, item.ExternalID, item.Name, item.Description,
			item.PriceMinor, item.WeightGrams, item.Position, item.IsAvailable)
		if err != nil {
			return fmt.Errorf("позиция %q: %w", item.ExternalID, err)
		}

		if err := applyOptionGroups(ctx, tx, item); err != nil {
			return err
		}
	}

	return nil
}

func applyOptionGroups(ctx context.Context, tx pgx.Tx, item ItemChange) error {
	// Удаление идёт первым: группы и опции могли не исчезнуть, а переехать,
	// и снятые записи не должны конфликтовать с новыми по (item_id, external_id).
	if len(item.RemoveGroupIDs) > 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM menu_option_groups WHERE id = ANY($1)`, item.RemoveGroupIDs); err != nil {
			return fmt.Errorf("удаление групп позиции %q: %w", item.ExternalID, err)
		}
	}

	for _, group := range item.Groups {
		_, err := tx.Exec(ctx, `
			INSERT INTO menu_option_groups (id, item_id, external_id, name, min_choices, max_choices, position)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (item_id, external_id) DO UPDATE
			SET name = EXCLUDED.name,
			    min_choices = EXCLUDED.min_choices,
			    max_choices = EXCLUDED.max_choices,
			    position = EXCLUDED.position,
			    updated_at = now()`,
			group.ID, item.ID, group.ExternalID, group.Name,
			group.MinChoices, group.MaxChoices, group.Position)
		if err != nil {
			return fmt.Errorf("группа %q: %w", group.ExternalID, err)
		}

		if len(group.RemoveOptionIDs) > 0 {
			if _, err := tx.Exec(ctx,
				`DELETE FROM menu_options WHERE id = ANY($1)`, group.RemoveOptionIDs); err != nil {
				return fmt.Errorf("удаление опций группы %q: %w", group.ExternalID, err)
			}
		}

		for _, option := range group.Options {
			_, err := tx.Exec(ctx, `
				INSERT INTO menu_options (id, group_id, external_id, name, price_delta_minor, position, is_available)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (group_id, external_id) DO UPDATE
				SET name = EXCLUDED.name,
				    price_delta_minor = EXCLUDED.price_delta_minor,
				    position = EXCLUDED.position,
				    is_available = EXCLUDED.is_available,
				    updated_at = now()`,
				option.ID, group.ID, option.ExternalID, option.Name,
				option.PriceDeltaMinor, option.Position, option.IsAvailable)
			if err != nil {
				return fmt.Errorf("опция %q: %w", option.ExternalID, err)
			}
		}
	}

	return nil
}

func archiveItems(ctx context.Context, tx pgx.Tx, venueID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}

	// venue_id в WHERE — даже здесь, где идентификаторы получены из своей же
	// выборки: правило изоляции не знает исключений.
	_, err := tx.Exec(ctx, `
		UPDATE menu_items SET is_archived = true, updated_at = now()
		WHERE id = ANY($1) AND venue_id = $2`, ids, venueID)
	if err != nil {
		return fmt.Errorf("архивация позиций: %w", err)
	}

	return nil
}

func writeImportAudit(
	ctx context.Context, tx pgx.Tx, venueID, importID uuid.UUID, payloadHash string, changes Changes,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO menu_imports (id, venue_id, payload_hash, items_created, items_updated,
		                          items_archived, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())`,
		importID, venueID, payloadHash, changes.Created, changes.Updated, changes.Archived)
	if err != nil {
		return fmt.Errorf("аудит импорта: %w", err)
	}

	return nil
}

// SetAvailability переключает стоп-лист позиций и опций одной транзакцией
// и сразу обновляет витрину поиска: снятая позиция обязана исчезнуть
// из выдачи за секунды, это сценарий M3.
//
// Возвращает цели, которым запрошен авто-возврат: их дальше отдаёт
// планировщику сервис — репозиторий про таймеры не знает.
func (r *Repository) SetAvailability(
	ctx context.Context, request AvailabilityRequest,
) (AvailabilityResult, []ScheduledReturn, error) {
	venueID, err := TenantFrom(ctx)
	if err != nil {
		return AvailabilityResult{}, nil, err
	}

	var (
		result    AvailabilityResult
		scheduled []ScheduledReturn
	)

	err = r.inTx(ctx, func(tx pgx.Tx) error {
		result = AvailabilityResult{}
		scheduled = nil

		for _, change := range request.Items {
			id, found, updateErr := setItemAvailability(ctx, tx, venueID, change)
			if updateErr != nil {
				return updateErr
			}

			if !found {
				result.NotFound = append(result.NotFound, change.ExternalID)

				continue
			}

			result.ItemsUpdated++
			scheduled = appendScheduled(scheduled, StopTarget{Kind: StopTargetItem, ID: id}, change)
		}

		for _, change := range request.Options {
			id, found, updateErr := setOptionAvailability(ctx, tx, venueID, change)
			if updateErr != nil {
				return updateErr
			}

			if !found {
				result.NotFound = append(result.NotFound, change.ExternalID)

				continue
			}

			result.OptionsUpdated++
			scheduled = appendScheduled(scheduled, StopTarget{Kind: StopTargetOption, ID: id}, change)
		}

		return refreshSearchItems(ctx, tx, venueID)
	})
	if err != nil {
		return AvailabilityResult{}, nil, err
	}

	return result, scheduled, nil
}

// ScheduledReturn — цель, для которой заведение попросило авто-возврат.
type ScheduledReturn struct {
	Target StopTarget
	Until  time.Time
}

func appendScheduled(targets []ScheduledReturn, target StopTarget, change AvailabilityChange) []ScheduledReturn {
	// Авто-возврат осмыслен только для снятия с продажи: возвращать
	// уже доступную позицию нечего.
	if change.Until == nil || change.IsAvailable {
		return targets
	}

	return append(targets, ScheduledReturn{Target: target, Until: *change.Until})
}

func setItemAvailability(
	ctx context.Context, tx pgx.Tx, venueID uuid.UUID, change AvailabilityChange,
) (uuid.UUID, bool, error) {
	var id uuid.UUID

	err := tx.QueryRow(ctx, `
		UPDATE menu_items SET is_available = $3, updated_at = now()
		WHERE venue_id = $1 AND external_id = $2 AND NOT is_archived
		RETURNING id`, venueID, change.ExternalID, change.IsAvailable).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}

	if err != nil {
		return uuid.Nil, false, fmt.Errorf("стоп-лист позиции %q: %w", change.ExternalID, err)
	}

	return id, true, nil
}

func setOptionAvailability(
	ctx context.Context, tx pgx.Tx, venueID uuid.UUID, change AvailabilityChange,
) (uuid.UUID, bool, error) {
	var id uuid.UUID

	// Опция принадлежит группе, группа — позиции, позиция — заведению:
	// фильтр по venue_id доезжает до опции через эту цепочку.
	err := tx.QueryRow(ctx, `
		UPDATE menu_options o SET is_available = $3, updated_at = now()
		FROM menu_option_groups g
		JOIN menu_items i ON i.id = g.item_id
		WHERE o.group_id = g.id AND i.venue_id = $1 AND o.external_id = $2
		RETURNING o.id`, venueID, change.ExternalID, change.IsAvailable).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}

	if err != nil {
		return uuid.Nil, false, fmt.Errorf("стоп-лист опции %q: %w", change.ExternalID, err)
	}

	return id, true, nil
}

// UpdateVenue применяет настройки заведения. Переданные поля заменяют текущие,
// непереданные остаются как были — PUT с одним полем не должен обнулять
// остальные.
func (r *Repository) UpdateVenue(ctx context.Context, settings VenueSettings) error {
	venueID, err := TenantFrom(ctx)
	if err != nil {
		return err
	}

	return r.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE venues SET
				is_accepting_orders    = COALESCE($2, is_accepting_orders),
				min_order_amount_minor = COALESCE($3, min_order_amount_minor),
				delivery_fee_minor     = COALESCE($4, delivery_fee_minor),
				delivery_radius_m      = COALESCE($5, delivery_radius_m),
				avg_prep_minutes       = COALESCE($6, avg_prep_minutes),
				timezone               = COALESCE($7, timezone),
				updated_at             = now()
			WHERE id = $1`,
			venueID, settings.IsAcceptingOrders, settings.MinOrderAmountMinor,
			settings.DeliveryFeeMinor, settings.DeliveryRadiusM,
			settings.AvgPrepMinutes, settings.Timezone)
		if err != nil {
			return fmt.Errorf("настройки заведения: %w", err)
		}

		if settings.WorkHours != nil {
			if err := replaceWorkHours(ctx, tx, venueID, settings.WorkHours); err != nil {
				return err
			}
		}

		// Пауза приёма и название заведения попадают в витрину поиска —
		// её нужно освежить вместе с настройками.
		return refreshSearchItems(ctx, tx, venueID)
	})
}

// replaceWorkHours заменяет расписание целиком: частичное обновление
// интервалов на практике всегда означает «пришли новое расписание»,
// а хранить историю правок расписания MVP не обязан.
func replaceWorkHours(ctx context.Context, tx pgx.Tx, venueID uuid.UUID, hours []WorkInterval) error {
	if _, err := tx.Exec(ctx, `DELETE FROM venue_work_hours WHERE venue_id = $1`, venueID); err != nil {
		return fmt.Errorf("очистка расписания: %w", err)
	}

	for _, interval := range hours {
		_, err := tx.Exec(ctx, `
			INSERT INTO venue_work_hours (id, venue_id, weekday, opens_at, closes_at)
			VALUES ($1, $2, $3, $4::time, $5::time)`,
			NewID(), venueID, interval.Weekday, interval.OpensAt, interval.ClosesAt)
		if err != nil {
			return fmt.Errorf("интервал расписания: %w", err)
		}
	}

	return nil
}

// UpsertWebhook регистрирует адрес и секрет подписи. Секрет хранится как есть:
// им подписываются исходящие вызовы, поэтому хэш здесь не подходит.
func (r *Repository) UpsertWebhook(ctx context.Context, webhook Webhook) (Webhook, error) {
	venueID, err := TenantFrom(ctx)
	if err != nil {
		return Webhook{}, err
	}

	var saved Webhook

	err = r.pool.QueryRow(ctx, `
		INSERT INTO venue_webhooks (venue_id, url, secret, is_active)
		VALUES ($1, $2, $3, true)
		ON CONFLICT (venue_id) DO UPDATE
		SET url = EXCLUDED.url, secret = EXCLUDED.secret, is_active = true, updated_at = now()
		RETURNING url, is_active`, venueID, webhook.URL, webhook.Secret).
		Scan(&saved.URL, &saved.IsActive)
	if err != nil {
		return Webhook{}, fmt.Errorf("регистрация вебхука: %w", err)
	}

	return saved, nil
}

// VenueSettingsSnapshot читает текущие настройки заведения — их возвращает
// ручка после применения изменений.
func (r *Repository) VenueSettingsSnapshot(ctx context.Context) (VenueSettings, error) {
	venueID, err := TenantFrom(ctx)
	if err != nil {
		return VenueSettings{}, err
	}

	var settings VenueSettings

	var (
		accepting bool
		minOrder  int64
		fee       int64
		radius    int
		prep      int
		timezone  string
	)

	err = r.pool.QueryRow(ctx, `
		SELECT is_accepting_orders, min_order_amount_minor, delivery_fee_minor,
		       delivery_radius_m, avg_prep_minutes, timezone
		FROM venues WHERE id = $1`, venueID).
		Scan(&accepting, &minOrder, &fee, &radius, &prep, &timezone)
	if err != nil {
		return VenueSettings{}, fmt.Errorf("чтение настроек: %w", err)
	}

	settings = VenueSettings{
		IsAcceptingOrders:   &accepting,
		MinOrderAmountMinor: &minOrder,
		DeliveryFeeMinor:    &fee,
		DeliveryRadiusM:     &radius,
		AvgPrepMinutes:      &prep,
		Timezone:            &timezone,
	}

	rows, err := r.pool.Query(ctx, `
		SELECT weekday, to_char(opens_at, 'HH24:MI'), to_char(closes_at, 'HH24:MI')
		FROM venue_work_hours WHERE venue_id = $1
		ORDER BY weekday, opens_at`, venueID)
	if err != nil {
		return VenueSettings{}, fmt.Errorf("чтение расписания: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var interval WorkInterval

		if err := rows.Scan(&interval.Weekday, &interval.OpensAt, &interval.ClosesAt); err != nil {
			return VenueSettings{}, fmt.Errorf("чтение интервала: %w", err)
		}

		settings.WorkHours = append(settings.WorkHours, interval)
	}

	if err := rows.Err(); err != nil {
		return VenueSettings{}, fmt.Errorf("чтение расписания: %w", err)
	}

	return settings, nil
}

// refreshSearchItems перестраивает витрину поиска для заведения.
//
// На этапе 3 витрина наполнялась только сидом, и любое изменение меню
// разъезжалось с выдачей поиска. Пока индексатор не появился (этап 6),
// витрина обновляется синхронно и в той же транзакции — иначе поиск покажет
// снятое блюдо, а каталог уже нет.
func refreshSearchItems(ctx context.Context, tx pgx.Tx, venueID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO search_items (item_id, venue_id, item_name, description, venue_name,
		                          category_name, price_minor, is_available, venue_is_active,
		                          venue_is_accepting, lat, lon, updated_at)
		SELECT i.id, i.venue_id, i.name, i.description, v.name, c.name, i.price_minor,
		       i.is_available, v.status = 'active', v.is_accepting_orders, v.lat, v.lon, now()
		FROM menu_items i
		JOIN venues v ON v.id = i.venue_id
		JOIN menu_categories c ON c.id = i.category_id
		WHERE i.venue_id = $1 AND NOT i.is_archived
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
			updated_at = now()`, venueID)
	if err != nil {
		return fmt.Errorf("обновление витрины поиска: %w", err)
	}

	// Архивированное и удалённое из витрины убирается: иначе поиск продолжит
	// показывать позицию, которой в меню больше нет.
	_, err = tx.Exec(ctx, `
		DELETE FROM search_items s
		WHERE s.venue_id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM menu_items i
		      WHERE i.id = s.item_id AND NOT i.is_archived
		  )`, venueID)
	if err != nil {
		return fmt.Errorf("очистка витрины поиска: %w", err)
	}

	return nil
}

// inTx выполняет работу в транзакции: коммит при успехе, откат при ошибке
// или панике.
func (r *Repository) inTx(ctx context.Context, do func(tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начало транзакции: %w", err)
	}

	defer func() {
		// Откат после успешного коммита — no-op, поэтому отдельного флага
		// «уже закоммитили» не нужно.
		_ = tx.Rollback(ctx)
	}()

	if err := do(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("коммит: %w", err)
	}

	return nil
}
