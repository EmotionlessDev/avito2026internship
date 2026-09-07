# Решения (ADR)

Здесь лежат решения, которые дорого менять и которые я ожидаю защищать на интервью:
у каждого есть контекст, цена и отвергнутые альтернативы. Мелкие решения сюда
не попадают — им место в комментарии рядом с кодом.

Формат один на все файлы: **Контекст** (что заставило выбирать), **Решение**
(что именно принято), **Последствия** (чем платим), **Отвергнутые альтернативы**
(почему не так). Статус у всех — «принято»: MVP реализован по ним целиком.

| № | Решение | Где видно в коде |
|---|---|---|
| [0001](0001-postgres-kafka-temporal.md) | Postgres — источник истины, Temporal — процесс заказа, Kafka — трансляция фактов | `internal/order`, `internal/order/workflow`, `internal/kafkax` |
| [0002](0002-transactional-outbox.md) | Публикация событий только через transactional outbox | `internal/outbox`, `migrations/0004_outbox_inbox.sql` |
| [0003](0003-money-bigint-minor.md) | Деньги — `bigint` в копейках, без `float` и `numeric` | все поля `*_minor` |
| [0004](0004-enum-text-check.md) | Перечисления — `text` + `CHECK`, а не Postgres `ENUM` | `migrations/*.sql` |
| [0005](0005-uuid-v7-in-app.md) | Идентификаторы — UUID v7, генерирует приложение | `internal/*/repository.go` |
| [0006](0006-mermaid-not-plantuml.md) | Диаграммы — Mermaid инлайном в markdown | `README.md`, `docs/DESIGN.md` |
| [0007](0007-postgres-fts-not-elasticsearch.md) | Поиск — Postgres FTS + `pg_trgm`, не Elasticsearch | `internal/search`, `migrations/0005_search.sql` |
| [0008](0008-order-items-snapshot.md) | Состав заказа — снапшот, а не ссылки на меню | `migrations/0003_orders.sql`, `internal/order` |

Правило синхронизации: если реализация расходится с решением — сначала правится
ADR, потом код. Разошедшийся ADR хуже отсутствующего: он врёт с видом документа.
