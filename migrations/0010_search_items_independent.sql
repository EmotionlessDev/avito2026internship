-- Витрина поиска перестаёт зависеть от таблицы, из которой её строят.
--
-- Внешние ключи на menu_items и venues достались витрине с этапа 3, когда её
-- наполнял каталог синхронно, в одной транзакции с меню. С этапа 6 её строит
-- консьюмер search-indexer из событий-снимков, и ключи стали противоречить
-- сути проекции: она обязана собираться из топика сама по себе, без
-- обращения к источнику (docs/adr/0007-postgres-fts-not-elasticsearch.md).
--
-- Практическая цена ключей: витрину нельзя пересобрать проигрыванием топика
-- на пустой базе, а событие, пришедшее раньше, чем видна строка меню,
-- уронило бы консьюмера ошибкой внешнего ключа вместо того, чтобы примениться.
-- Ровно поэтому у проекций внешних ключей не держат.
--
-- Каскадное удаление вместе с ними уходит, и это осознанно: позиции меню
-- удаляются мягко (is_archived), а событие об архивации индексатор
-- обрабатывает сам — удаляет строку из витрины.

-- +goose Up

ALTER TABLE search_items DROP CONSTRAINT search_items_item_id_fkey;
ALTER TABLE search_items DROP CONSTRAINT search_items_venue_id_fkey;

COMMENT ON TABLE search_items IS
    'Проекция catalog-events: собирается из событий-снимков и не ссылается на источник';

-- +goose Down

DELETE FROM search_items s
WHERE NOT EXISTS (SELECT 1 FROM menu_items i WHERE i.id = s.item_id)
   OR NOT EXISTS (SELECT 1 FROM venues v WHERE v.id = s.venue_id);

ALTER TABLE search_items
    ADD CONSTRAINT search_items_item_id_fkey
    FOREIGN KEY (item_id) REFERENCES menu_items (id) ON DELETE CASCADE;

ALTER TABLE search_items
    ADD CONSTRAINT search_items_venue_id_fkey
    FOREIGN KEY (venue_id) REFERENCES venues (id) ON DELETE CASCADE;
