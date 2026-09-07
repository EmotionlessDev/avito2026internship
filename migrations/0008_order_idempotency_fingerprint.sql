-- Отпечаток запроса на оформление.
--
-- Ключ идемпотентности защищает от повтора того же запроса. Но тот же ключ
-- с другим телом — это не ретрай, а ошибка клиента, и вернуть на него старый
-- заказ значит тихо проигнорировать новые данные (другой адрес, другой
-- телефон). Контракт обещает на такой случай 409, а отличить один случай
-- от другого можно только по отпечатку исходного запроса.

-- +goose Up

ALTER TABLE orders ADD COLUMN idempotency_fingerprint text NOT NULL DEFAULT '';

COMMENT ON COLUMN orders.idempotency_fingerprint IS 'sha256 нормализованного тела запроса на оформление';

-- +goose Down

ALTER TABLE orders DROP COLUMN idempotency_fingerprint;
