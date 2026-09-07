-- Заведения, их доступы, расписание и меню.
--
-- Связь данных платформы и заведения держится на UNIQUE (venue_id, external_id):
-- заведение шлёт импорт в своих артикулах и ничего не знает про наши uuid
-- (обоснование — README.md, раздел «О проекте»).

-- +goose Up

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE venues (
    id                     uuid PRIMARY KEY,
    slug                   text        NOT NULL UNIQUE,
    name                   text        NOT NULL,
    address_text           text        NOT NULL,
    -- Координаты точки заведения: зона доставки в MVP — радиус, а не полигон
    -- (упрощение из §11, схема уже готова к переезду на PostGIS).
    lat                    double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
    lon                    double precision NOT NULL CHECK (lon BETWEEN -180 AND 180),
    -- IANA-имя зоны: в базе всё в UTC, часовой пояс нужен только расписанию.
    timezone               text        NOT NULL DEFAULT 'Europe/Moscow',
    status                 text        NOT NULL DEFAULT 'active'
                                       CHECK (status IN ('active', 'suspended', 'draft')),
    -- Пауза приёма (сценарий M7): заведение остаётся в каталоге, но помечено.
    is_accepting_orders    boolean     NOT NULL DEFAULT true,
    min_order_amount_minor bigint      NOT NULL DEFAULT 0 CHECK (min_order_amount_minor >= 0),
    delivery_fee_minor     bigint      NOT NULL DEFAULT 0 CHECK (delivery_fee_minor >= 0),
    delivery_radius_m      integer     NOT NULL DEFAULT 5000 CHECK (delivery_radius_m > 0),
    avg_prep_minutes       integer     NOT NULL DEFAULT 30 CHECK (avg_prep_minutes > 0),
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE venues IS 'Заведение: ресторан, кафе или магазин готовой еды';
COMMENT ON COLUMN venues.is_accepting_orders IS 'Пауза кухни в час пик, переключает само заведение';

-- Ключ хранится только хэшем: утечка дампа не даёт доступа к Merchant API.
-- Префикс — чтобы найти строку по ключу одним индексным поиском, не сравнивая
-- хэш со всеми строками таблицы.
CREATE TABLE venue_api_keys (
    id         uuid PRIMARY KEY,
    venue_id   uuid        NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    name       text        NOT NULL DEFAULT '',
    key_prefix text        NOT NULL,
    key_hash   text        NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

CREATE INDEX idx_api_keys_prefix ON venue_api_keys (key_prefix) WHERE revoked_at IS NULL;

-- Несколько интервалов на день допустимы осознанно: обед и ужин с перерывом —
-- обычное дело. closes_at <= opens_at означает работу через полночь.
CREATE TABLE venue_work_hours (
    id        uuid PRIMARY KEY,
    venue_id  uuid     NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    weekday   smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    opens_at  time     NOT NULL,
    closes_at time     NOT NULL
);

COMMENT ON COLUMN venue_work_hours.weekday IS '0 = понедельник, 6 = воскресенье';
COMMENT ON COLUMN venue_work_hours.closes_at IS 'Меньше или равно opens_at — заведение работает через полночь';

CREATE INDEX idx_work_hours_venue ON venue_work_hours (venue_id, weekday);

-- Один активный вебхук на заведение, поэтому venue_id — сам первичный ключ.
CREATE TABLE venue_webhooks (
    venue_id   uuid PRIMARY KEY REFERENCES venues (id) ON DELETE CASCADE,
    url        text        NOT NULL,
    secret     text        NOT NULL,
    is_active  boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON COLUMN venue_webhooks.secret IS 'Секрет HMAC-подписи X-Kitchen-Signature';

CREATE TABLE menu_categories (
    id          uuid PRIMARY KEY,
    venue_id    uuid        NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    external_id text        NOT NULL,
    name        text        NOT NULL,
    position    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    UNIQUE (venue_id, external_id)
);

CREATE INDEX idx_categories_venue_position ON menu_categories (venue_id, position);

CREATE TABLE menu_items (
    id           uuid PRIMARY KEY,
    venue_id     uuid        NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    category_id  uuid        NOT NULL REFERENCES menu_categories (id) ON DELETE RESTRICT,
    external_id  text        NOT NULL,
    name         text        NOT NULL,
    description  text        NOT NULL DEFAULT '',
    price_minor  bigint      NOT NULL CHECK (price_minor >= 0),
    weight_grams integer     CHECK (weight_grams IS NULL OR weight_grams > 0),
    position     integer     NOT NULL DEFAULT 0,
    -- Стоп-лист (M3): позиция есть в меню, но сейчас недоступна.
    is_available boolean     NOT NULL DEFAULT true,
    -- Мягкое удаление: на позицию ссылаются исторические заказы.
    is_archived  boolean     NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    UNIQUE (venue_id, external_id)
);

COMMENT ON COLUMN menu_items.is_archived IS 'Позиция исчезла из снапшота меню заведения';

-- Выдача меню: только живые позиции, сразу в порядке показа.
CREATE INDEX idx_items_venue_active ON menu_items (venue_id, category_id, position)
    WHERE NOT is_archived;

CREATE TABLE menu_option_groups (
    id          uuid PRIMARY KEY,
    item_id     uuid        NOT NULL REFERENCES menu_items (id) ON DELETE CASCADE,
    external_id text        NOT NULL,
    name        text        NOT NULL,
    min_choices integer     NOT NULL DEFAULT 0 CHECK (min_choices >= 0),
    max_choices integer     NOT NULL DEFAULT 1 CHECK (max_choices >= min_choices),
    position    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    UNIQUE (item_id, external_id)
);

COMMENT ON TABLE menu_option_groups IS 'Группа модификаторов позиции: «Размер», «Добавки»';

CREATE TABLE menu_options (
    id                uuid PRIMARY KEY,
    group_id          uuid        NOT NULL REFERENCES menu_option_groups (id) ON DELETE CASCADE,
    external_id       text        NOT NULL,
    name              text        NOT NULL,
    -- Может быть отрицательной: «без сыра, минус 50 ₽» — тоже модификатор.
    price_delta_minor bigint      NOT NULL DEFAULT 0,
    position          integer     NOT NULL DEFAULT 0,
    -- Стоп-лист на уровне опции — сценарий U13.
    is_available      boolean     NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    UNIQUE (group_id, external_id)
);

-- Аудит синхронизаций меню. Уникальности по payload_hash нет намеренно:
-- заведение имеет право откатить меню к предыдущему снапшоту, и такой импорт
-- обязан записаться. Идемпотентность считается по последнему импорту заведения.
CREATE TABLE menu_imports (
    id             uuid PRIMARY KEY,
    venue_id       uuid        NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    payload_hash   text        NOT NULL,
    items_created  integer     NOT NULL DEFAULT 0,
    items_updated  integer     NOT NULL DEFAULT 0,
    items_archived integer     NOT NULL DEFAULT 0,
    started_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz
);

CREATE INDEX idx_menu_imports_venue ON menu_imports (venue_id, started_at DESC);

-- +goose Down

DROP TABLE menu_imports;
DROP TABLE menu_options;
DROP TABLE menu_option_groups;
DROP TABLE menu_items;
DROP TABLE menu_categories;
DROP TABLE venue_webhooks;
DROP TABLE venue_work_hours;
DROP TABLE venue_api_keys;
DROP TABLE venues;

DROP EXTENSION IF EXISTS pg_trgm;
