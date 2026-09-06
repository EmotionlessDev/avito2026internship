-- Витрина поиска блюд.
--
-- Пользователь ищет блюдо, а не заведение (docs/DESIGN.md §9), поэтому выдача
-- собирается из одной денормализованной таблицы: позиция уже склеена
-- с заведением, категорией и признаком доступности. На этапе 3 витрину
-- наполняет каталог синхронно, с этапа 6 — консьюмер search-indexer из Kafka.

-- +goose Up

CREATE TABLE search_items (
    -- Ключ витрины — сама позиция меню: одна строка на menu_item.
    item_id            uuid PRIMARY KEY REFERENCES menu_items (id) ON DELETE CASCADE,
    venue_id           uuid   NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    item_name          text   NOT NULL,
    description        text   NOT NULL DEFAULT '',
    venue_name         text   NOT NULL,
    category_name      text   NOT NULL DEFAULT '',
    price_minor        bigint NOT NULL CHECK (price_minor >= 0),
    -- Недоступное не выбрасывается из выдачи, а опускается вниз с флагом:
    -- пользователю полезно знать, что блюдо здесь бывает.
    is_available       boolean NOT NULL DEFAULT true,
    venue_is_active    boolean NOT NULL DEFAULT true,
    -- Пауза приёма заказов заведением — сценарий M7.
    venue_is_accepting boolean NOT NULL DEFAULT true,
    lat                double precision NOT NULL,
    lon                double precision NOT NULL,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    -- Вес A у названия блюда, B у названия заведения, C у описания:
    -- совпадение в названии должно перевешивать совпадение в описании.
    search_vector      tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('russian', coalesce(item_name, '')), 'A') ||
        setweight(to_tsvector('russian', coalesce(venue_name, '')), 'B') ||
        setweight(to_tsvector('russian', coalesce(description, '')), 'C')
    ) STORED
);

-- Полнотекстовый поиск с русским стеммингом.
CREATE INDEX idx_search_vector ON search_items USING GIN (search_vector);

-- Опечатки (U12): «хачапур» → «Хачапури по-аджарски». Триграммы — fallback
-- к полнотексту, когда тот вернул мало результатов.
CREATE INDEX idx_search_trgm ON search_items USING GIN (item_name gin_trgm_ops);

-- Переиндексация меню заведения и фильтр выдачи по заведению.
CREATE INDEX idx_search_venue ON search_items (venue_id);

-- +goose Down

DROP TABLE search_items;
