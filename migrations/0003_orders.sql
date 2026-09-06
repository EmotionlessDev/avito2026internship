-- Заказы: сам заказ, снапшот состава, аудит переходов и платёж.
--
-- orders.status — проекция состояния workflow (главный ADR, docs/DESIGN.md §5):
-- пишут его только activities, читает HTTP API. Переходы разрешает матрица FSM
-- в internal/order, база держит только список допустимых значений.

-- +goose Up

CREATE TABLE orders (
    id                  uuid PRIMARY KEY,
    -- Человекочитаемый номер для поддержки и для повара: К-240915-0042.
    number              text        NOT NULL UNIQUE,
    user_id             uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    venue_id            uuid        NOT NULL REFERENCES venues (id) ON DELETE RESTRICT,
    status              text        NOT NULL
                                    CHECK (status IN (
                                        'created',
                                        'payment_failed',
                                        'placed',
                                        'accepted',
                                        'cooking',
                                        'ready',
                                        'delivering',
                                        'completed',
                                        'rejected',
                                        'expired',
                                        'cancelled'
                                    )),
    items_amount_minor  bigint      NOT NULL CHECK (items_amount_minor >= 0),
    delivery_fee_minor  bigint      NOT NULL DEFAULT 0 CHECK (delivery_fee_minor >= 0),
    total_amount_minor  bigint      NOT NULL CHECK (total_amount_minor >= 0),
    delivery_address    text        NOT NULL,
    delivery_lat        double precision NOT NULL CHECK (delivery_lat BETWEEN -90 AND 90),
    delivery_lon        double precision NOT NULL CHECK (delivery_lon BETWEEN -180 AND 180),
    contact_phone       text        NOT NULL,
    -- Заполняется заведением при accept.
    eta_minutes         integer     CHECK (eta_minutes IS NULL OR eta_minutes > 0),
    -- Ключ идемпотентности оформления: повтор возвращает тот же заказ и 200.
    idempotency_key     text        NOT NULL,
    -- Связь с Temporal: "order-" + id. Дубликат заказа невозможен физически.
    workflow_id         text        NOT NULL,
    cancel_reason       text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    -- Момент ухода заказа в заведение и момент завершения: нужны витрине
    -- статистики (M8) и разбору SLA, из истории статусов их доставать дороже.
    placed_at           timestamptz,
    completed_at        timestamptz,

    UNIQUE (user_id, idempotency_key)
);

-- Лента заказов заведения: поллинг-фолбэк POS (M6) и экран повара.
CREATE INDEX idx_orders_venue_status ON orders (venue_id, status, created_at DESC);

-- История заказов пользователя.
CREATE INDEX idx_orders_user ON orders (user_id, created_at DESC);

-- Состав заказа — снапшот, а не ссылка: чек выданного заказа не меняется,
-- даже если заведение подняло цену или архивировало позицию (§7.2).
CREATE TABLE order_items (
    id                    uuid PRIMARY KEY,
    order_id              uuid    NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    -- Ссылка остаётся для аналитики; отображаемые и денежные поля скопированы.
    menu_item_id          uuid    NOT NULL REFERENCES menu_items (id) ON DELETE RESTRICT,
    -- Артикул заведения: POS должен получить заказ в своих идентификаторах.
    external_item_id      text    NOT NULL,
    name_snapshot         text    NOT NULL,
    price_minor_snapshot  bigint  NOT NULL CHECK (price_minor_snapshot >= 0),
    qty                   integer NOT NULL CHECK (qty > 0),
    options_snapshot      jsonb   NOT NULL DEFAULT '[]'::jsonb,
    total_minor           bigint  NOT NULL CHECK (total_minor >= 0)
);

CREATE INDEX idx_order_items_order ON order_items (order_id);

-- Аудит переходов: таймлайн для UI, материал для разбора инцидентов
-- и источник событий для SSE-потока статусов.
CREATE TABLE order_status_history (
    id          uuid PRIMARY KEY,
    order_id    uuid        NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    -- NULL у самой первой записи: у создания заказа нет предыдущего статуса.
    from_status text,
    to_status   text        NOT NULL,
    actor_type  text        NOT NULL CHECK (actor_type IN ('user', 'venue', 'system')),
    reason      text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_status_history_order ON order_status_history (order_id, created_at);

-- Платёж замокан (§11), но холд и списание разнесены по-настоящему:
-- authorize при оформлении, capture при доставке, void при отказе заведения.
CREATE TABLE payments (
    id           uuid PRIMARY KEY,
    order_id     uuid        NOT NULL UNIQUE REFERENCES orders (id) ON DELETE CASCADE,
    status       text        NOT NULL CHECK (status IN ('authorized', 'captured', 'voided')),
    amount_minor bigint      NOT NULL CHECK (amount_minor >= 0),
    provider_ref text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE payments;
DROP TABLE order_status_history;
DROP TABLE order_items;
DROP TABLE orders;
