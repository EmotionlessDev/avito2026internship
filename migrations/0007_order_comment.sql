-- Комментарий пользователя к заказу.
--
-- Поле есть в обоих контурах API («позвонить за 10 минут», «без лука»),
-- и его читает повар, поэтому оно хранится вместе с заказом, а не теряется
-- по дороге в POS.

-- +goose Up

ALTER TABLE orders ADD COLUMN comment text NOT NULL DEFAULT '';

COMMENT ON COLUMN orders.comment IS 'Комментарий пользователя к заказу; пустая строка — комментария нет';

-- +goose Down

ALTER TABLE orders DROP COLUMN comment;
