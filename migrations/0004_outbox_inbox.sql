-- Инфраструктура надёжной доставки событий.
--
-- Событие нельзя опубликовать в Kafka «после коммита»: между COMMIT и Produce
-- процесс может умереть (docs/DESIGN.md §5). Поэтому запись в outbox идёт
-- в той же транзакции, что и данные, а relay публикует её отдельно.

-- +goose Up

CREATE TABLE outbox (
    -- bigserial, а не uuid: relay читает записи в порядке идентификатора.
    -- Глобальный порядок публикации при этом не гарантирован (§5) и не нужен —
    -- гарантирован порядок per-aggregate через ключ партиции Kafka.
    id             bigserial PRIMARY KEY,
    -- Ключ дедупликации у консьюмера, генерирует приложение (UUID v7).
    event_id       uuid        NOT NULL UNIQUE,
    aggregate_type text        NOT NULL
                               CHECK (aggregate_type IN ('order', 'menu_item', 'menu_option', 'venue')),
    -- Он же ключ партиции Kafka: события одного заказа приходят по порядку.
    aggregate_id   uuid        NOT NULL,
    event_type     text        NOT NULL,
    payload        jsonb       NOT NULL,
    -- trace_id и прочий контекст переноса, отдельно от бизнес-данных.
    headers        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- NULL — ещё не отправлено. Опубликованные строки остаются для разбора
    -- инцидентов и чистятся отдельным ретеншном.
    published_at   timestamptz,
    attempts       integer     NOT NULL DEFAULT 0,
    last_error     text
);

COMMENT ON COLUMN outbox.event_type IS 'Полное имя события: order.placed, menu_item.updated';

-- Единственный индекс, который нужен relay: хвост неопубликованных записей.
-- Частичный — опубликованные строки в него не попадают и не растят его.
CREATE INDEX idx_outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- LISTEN/NOTIFY снимает задержку опроса: relay просыпается на вставке,
-- а не ждёт следующего тика. Опрос остаётся страховкой — уведомление
-- не переживает разрыв соединения, а строка в таблице переживает.
-- +goose StatementBegin
CREATE FUNCTION outbox_notify() RETURNS trigger
    LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_notify('outbox_new', '');

    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- FOR EACH STATEMENT: одно уведомление на вставку любого размера. Relay всё
-- равно вычитывает пачку, будить его построчно смысла нет.
CREATE TRIGGER trg_outbox_notify
    AFTER INSERT ON outbox
    FOR EACH STATEMENT
EXECUTE FUNCTION outbox_notify();

-- Приёмная сторона: at-least-once у Kafka — норма, поэтому консьюмер пишет
-- event_id в той же транзакции, что и проекцию. Повторная доставка не приводит
-- к двойному применению.
CREATE TABLE processed_events (
    consumer     text        NOT NULL,
    event_id     uuid        NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (consumer, event_id)
);

COMMENT ON COLUMN processed_events.consumer IS 'Имя группы консьюмера: search-indexer, notifier, merchant-stats';

-- +goose Down

DROP TABLE processed_events;
DROP TRIGGER trg_outbox_notify ON outbox;
DROP FUNCTION outbox_notify();
DROP TABLE outbox;
