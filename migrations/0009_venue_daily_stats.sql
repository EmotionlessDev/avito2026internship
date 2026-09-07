-- Витрина статистики заведения (сценарий M8).
--
-- Отдельная таблица, а не агрегат по orders на лету: витрину наполняет
-- консьюмер order-events, и это ровно то разделение, ради которого в системе
-- есть Kafka — считать за день по горячей таблице заказов значит нагружать
-- операционный путь ради отчёта (docs/DESIGN.md §5).
--
-- Ключ — заведение и календарный день в его часовом поясе: «за сегодня»
-- для владельца заведения означает его сегодня, а не UTC.

-- +goose Up

CREATE TABLE venue_daily_stats (
    venue_id             uuid        NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    stat_date            date        NOT NULL,
    orders_total         integer     NOT NULL DEFAULT 0 CHECK (orders_total >= 0),
    orders_completed     integer     NOT NULL DEFAULT 0 CHECK (orders_completed >= 0),
    orders_rejected      integer     NOT NULL DEFAULT 0 CHECK (orders_rejected >= 0),
    orders_expired       integer     NOT NULL DEFAULT 0 CHECK (orders_expired >= 0),
    revenue_minor        bigint      NOT NULL DEFAULT 0 CHECK (revenue_minor >= 0),
    -- Среднее время подтверждения хранится слагаемыми: среднее нельзя
    -- досчитать инкрементально, а сумма и счётчик — можно.
    accept_seconds_total bigint      NOT NULL DEFAULT 0 CHECK (accept_seconds_total >= 0),
    accept_count         integer     NOT NULL DEFAULT 0 CHECK (accept_count >= 0),
    updated_at           timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (venue_id, stat_date)
);

COMMENT ON TABLE venue_daily_stats IS 'Проекция order-events: показатели заведения за календарный день';
COMMENT ON COLUMN venue_daily_stats.stat_date IS 'День в часовом поясе заведения, а не в UTC';
COMMENT ON COLUMN venue_daily_stats.revenue_minor IS 'Выручка по завершённым заказам, копейки';
COMMENT ON COLUMN venue_daily_stats.accept_seconds_total IS 'Сумма времён от placed до accepted';

-- +goose Down

DROP TABLE venue_daily_stats;
