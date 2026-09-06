-- Пользователи и корзина.
--
-- Авторизации в MVP нет: user_id приходит в заголовке X-User-Id и резолвится
-- в строку этой таблицы (упрощение из docs/DESIGN.md §11).

-- +goose Up

CREATE TABLE users (
    id         uuid PRIMARY KEY,
    phone      text        NOT NULL,
    name       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_users_phone ON users (phone);

-- Одна активная корзина на пользователя — продуктовое решение (§11).
-- Технически снимается заменой UNIQUE (user_id) на UNIQUE (user_id, venue_id).
CREATE TABLE carts (
    id         uuid PRIMARY KEY,
    user_id    uuid        NOT NULL UNIQUE REFERENCES users (id) ON DELETE CASCADE,
    -- NULL — пустая корзина: заведение появляется вместе с первой позицией
    -- и определяет, что вторая позиция из другого заведения даст CART_MIXED_VENUES.
    venue_id   uuid        REFERENCES venues (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE cart_items (
    id                uuid PRIMARY KEY,
    cart_id           uuid        NOT NULL REFERENCES carts (id) ON DELETE CASCADE,
    menu_item_id      uuid        NOT NULL REFERENCES menu_items (id) ON DELETE CASCADE,
    qty               integer     NOT NULL CHECK (qty > 0),
    -- Выбранные модификаторы: [{"group_external_id": "...", "option_ids": [...]}].
    -- jsonb, а не таблица: состав опций читается и пишется только целиком.
    options           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    -- Цена позиции на момент добавления — по ней детектируется PRICE_CHANGED (U7).
    added_price_minor bigint      NOT NULL CHECK (added_price_minor >= 0),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_cart_items_cart ON cart_items (cart_id, created_at);

-- +goose Down

DROP TABLE cart_items;
DROP TABLE carts;
DROP TABLE users;
