# Авито.Кухня — MVP

Платформа доставки еды внутри Авито: закрытый пул заведений подключается по API,
пользователи ищут блюда, собирают корзину и оформляют заказ. Заведение в этом
репозитории тоже есть — `merchant-pos` — отдельный сервис со своим хранилищем,
который общается с платформой только по опубликованным контрактам.

Всё поднимается одной командой и прогоняется одной: 22 бизнес-сценария
с ассертами, включая «трудности» — заведение отказало, промолчало, сняло блюдо
со стопа, подняло цену, оказалось недоступно.

## Быстрый старт

```bash
make up      # стенд: 8 сервисов, миграции и демо-данные накатываются сами
make demo    # 22 сценария: 14 пользовательских (U1–U14) и 8 ресторанных (M1–M8)
make down    # погасить вместе с томами
```

`make demo` — не набор curl'ов для чтения глазами, а тест: каждый шаг проверяет
код ответа и поля JSON, расхождение валит прогон. Это и есть доказательство
критерия приёмки «приложение выполняет все бизнес-сценарии».

Сценарий U10 ждёт таймер подтверждения заказа — по умолчанию семь минут, как
в дизайне, поэтому полный прогон занимает около девяти минут. Быстрый вариант:
`ORDER_CONFIRMATION_TIMEOUT=25s make up && make demo` — около двух.

## О проекте

Задача: MVP платформы доставки еды. Заведения из закрытого пула подключаются
через API — присылают меню, получают заказы, ведут их до выдачи. Пользователи
ищут блюда по всем заведениям сразу, собирают корзину и оформляют заказ.
Авторизация, клиентская часть, реальные платежи и курьеры — вне MVP.

Главный вопрос, вокруг которого построена система: **чем связать данные платформы
с данными заведения**. У заведения свои артикулы из его учётной системы, у нас
свои `uuid`. Ответ — составной естественный ключ `UNIQUE(venue_id, external_id)`;
что он даёт и что было бы без него — в разделе [Схема БД](#заведения-и-меню).

Второй вопрос — **что считать состоянием заказа**, когда в системе три технологии,
умеющие хранить состояние. Postgres — источник истины и все чтения; Temporal —
процесс одного заказа с таймерами, сигналами и компенсациями; Kafka — трансляция
фактов наблюдателям. Разбор — [ADR 0001](docs/adr/0001-postgres-kafka-temporal.md).

## Документы

- [docs/DESIGN.md](docs/DESIGN.md) — дизайн системы: CJM покупателя (§2) и
  заведения (§3) с полным разбором сценариев, жизненный цикл заказа, состав API.
- [docs/adr/](docs/adr/) — решения, которые дорого менять: контекст, цена,
  отвергнутые альтернативы.
- [PLAN.md](PLAN.md) — план реализации по этапам; он же приложенный «промпт (plan)».
- [backend-trainee-assignment-autumn-2026.md](backend-trainee-assignment-autumn-2026.md) — текст задания.

## Архитектура

Диаграммы в нотации C4, три уровня: система в окружении, контейнеры внутри неё,
компоненты самого нагруженного модуля. Всё — Mermaid инлайном, GitHub рендерит
их прямо здесь ([ADR 0006](docs/adr/0006-mermaid-not-plantuml.md)).

### Level 1 — контекст

```mermaid
flowchart TB
    user["👤 <b>Покупатель</b><br/><i>«Person»</i><br/>Ищет и заказывает еду"]
    staff["👤 <b>Сотрудник заведения</b><br/><i>«Person»</i><br/>Ведёт меню и заказы"]

    kitchen["<b>Авито.Кухня</b><br/><i>«Software System»</i><br/>Каталог, заказы, интеграция<br/>с заведениями"]

    pos["<b>POS заведения</b><br/><i>«External System»</i><br/>Собственная учётная система<br/>ресторана"]
    avito["<b>Авито Core</b><br/><i>«External System»</i><br/>Профили, авторизация<br/><small>вне MVP — заглушка</small>"]
    pay["<b>Платёжный провайдер</b><br/><i>«External System»</i><br/><small>вне MVP — мок</small>"]

    user -->|"HTTPS / JSON<br/>смотрит каталог,<br/>оформляет заказ"| kitchen
    staff -->|"работает в своей системе"| pos
    pos <-->|"HTTPS / JSON<br/>меню, стоп-листы,<br/>заказы"| kitchen
    kitchen -.->|"user_id из JWT"| avito
    kitchen -.->|"hold / capture / void"| pay

    style kitchen fill:#1168bd,stroke:#0b4884,color:#fff
    style user fill:#08427b,stroke:#052e56,color:#fff
    style staff fill:#08427b,stroke:#052e56,color:#fff
    style pos fill:#999,stroke:#6b6b6b,color:#fff
    style avito fill:#999,stroke:#6b6b6b,color:#fff
    style pay fill:#999,stroke:#6b6b6b,color:#fff
```

### Level 2 — контейнеры

```mermaid
flowchart TB
    user["👤 Покупатель<br/><i>«Person»</i>"]

    subgraph sys["Авито.Кухня"]
        direction TB
        api["<b>kitchen-api</b><br/><i>«Container: Go, chi»</i><br/>Public + Merchant HTTP API,<br/>валидация, корзина"]
        worker["<b>kitchen-worker</b><br/><i>«Container: Go»</i><br/>Temporal worker, Kafka-консьюмеры,<br/>outbox-relay"]
        db[("<b>PostgreSQL 16</b><br/><i>«Container: Database»</i><br/>Источник истины:<br/>каталог, заказы, outbox")]
        kafka[["<b>Kafka (KRaft)</b><br/><i>«Container: Message Broker»</i><br/>Шина доменных событий"]]
        temporal["<b>Temporal</b><br/><i>«Container: Workflow Engine»</i><br/>Durable-оркестрация<br/>жизненного цикла заказа"]
    end

    subgraph demo["Демо-стенд (тот же репозиторий)"]
        pos["<b>merchant-pos</b><br/><i>«Container: Go + SQLite»</i><br/>Пример заведения:<br/>своё меню, свои заказы"]
    end

    user -->|"JSON/HTTPS"| api
    api -->|"SQL, pgx"| db
    api -->|"StartWorkflow<br/>SignalWorkflow"| temporal
    worker -->|"SQL"| db
    worker -->|"produce / consume"| kafka
    worker -->|"poll task queue"| temporal
    temporal -->|"activities"| worker
    worker -->|"webhook + HMAC<br/>новый заказ"| pos
    pos -->|"меню, стоп-листы,<br/>accept/reject/ready"| api

    style api fill:#1168bd,stroke:#0b4884,color:#fff
    style worker fill:#1168bd,stroke:#0b4884,color:#fff
    style pos fill:#2d8659,stroke:#1d5c3c,color:#fff
    style db fill:#438dd5,stroke:#2e6295,color:#fff
    style kafka fill:#438dd5,stroke:#2e6295,color:#fff
    style temporal fill:#438dd5,stroke:#2e6295,color:#fff
    style user fill:#08427b,stroke:#052e56,color:#fff
```

**Почему API и Worker — разные контейнеры.** У них разный профиль нагрузки и разные
причины падать. API обязан отвечать за миллисекунды и масштабируется по RPS; worker
держит долгие соединения с Temporal и Kafka и масштабируется по объёму событий.
Падение worker'а не должно ронять выдачу каталога — заказы просто накапливаются в outbox.

### Level 3 — компоненты `kitchen-api`, модуль заказа

```mermaid
flowchart TB
    subgraph api["kitchen-api"]
        direction TB
        mw["<b>httpx.Middleware</b><br/>request-id, recovery, logging,<br/>user/api-key auth, problem+json"]

        oh["<b>OrderHandler</b><br/><i>«Component»</i><br/>POST /v1/orders<br/>POST /v1/orders/:id/cancel"]
        mh["<b>MerchantOrderHandler</b><br/><i>«Component»</i><br/>accept / reject / ready"]

        os["<b>OrderService</b><br/><i>«Component»</i><br/>Оркестрация создания заказа,<br/>идемпотентность"]
        cv["<b>CartValidator</b><br/><i>«Component»</i><br/>Каскад проверок → issues[]"]
        fsm["<b>order.StateMachine</b><br/><i>«Component»</i><br/>Матрица переходов,<br/>кто имеет право"]

        orepo["<b>OrderRepository</b><br/><i>«Component»</i><br/>pgx, транзакции"]
        obox["<b>OutboxWriter</b><br/><i>«Component»</i><br/>Пишет событие в той же<br/>транзакции, что и заказ"]
        tcl["<b>TemporalClient</b><br/><i>«Component»</i><br/>StartWorkflow / Signal"]
    end

    db[("PostgreSQL")]
    tmp["Temporal"]

    mw --> oh & mh
    oh --> os
    mh --> fsm
    os --> cv
    os --> fsm
    os --> orepo
    os --> obox
    os --> tcl
    mh --> tcl
    orepo --> db
    obox --> db
    tcl --> tmp

    style os fill:#1168bd,stroke:#0b4884,color:#fff
    style cv fill:#1168bd,stroke:#0b4884,color:#fff
    style fsm fill:#1168bd,stroke:#0b4884,color:#fff
```

### Кто за что отвечает

Главное решение архитектуры — граница между тремя технологиями, каждая из которых
умеет хранить состояние. Без явной границы на вопрос «какой сейчас статус заказа»
появилось бы три ответа.

| Что | Роль | Чего **не** делает |
|---|---|---|
| **PostgreSQL** | Источник истины. Все чтения идут сюда | Не хранит логику процесса: `orders.status` — проекция, её пишут только activities |
| **Temporal** | Процесс одного заказа: durable-таймеры, сигналы, компенсации | Не отвечает на запросы пользователя — для этого есть проекция в Postgres |
| **Kafka** | Трансляция фактов наблюдателям: поиск, уведомления, статистика | Ни один запрос пользователя её не ждёт, ни одно решение по ней не принимается |

Практическое следствие: заказ доставляет в POS **activity Temporal**, а не
Kafka-консьюмер, потому что процессу нужен результат вызова, чтобы решать, что
делать дальше. Наблюдателям результат не нужен — они на Kafka. Разбор с
отвергнутыми альтернативами — [ADR 0001](docs/adr/0001-postgres-kafka-temporal.md).

Жизненный цикл заказа, машина состояний и sequence-диаграмма `OrderWorkflow` —
в [DESIGN §6](docs/DESIGN.md#6-жизненный-цикл-заказа).

## Схема БД

20 таблиц, PostgreSQL 16. Миграции — goose, встроены в бинарь через `embed`,
у каждой есть и `Up`, и `Down`; откат проверяется (`make migrate-reset`).

Общие правила схемы:

| Правило | Почему |
|---|---|
| Деньги — `bigint` в копейках (`*_minor`) | Точная арифметика без `float`; в API тоже целые ([ADR 0003](docs/adr/0003-money-bigint-minor.md)) |
| Идентификаторы — `uuid` v7, генерирует приложение | K-sortable: вставки ложатся в конец индекса, id известен до коммита ([ADR 0005](docs/adr/0005-uuid-v7-in-app.md)) |
| Перечисления — `text` + `CHECK` | Добавить значение — обычная миграция; из `ENUM` значение нельзя удалить ([ADR 0004](docs/adr/0004-enum-text-check.md)) |
| Время — `timestamptz`, всегда UTC | Часовой пояс заведения хранится отдельно и применяется только при расчёте расписания |
| Удаление — мягкое (`is_archived`) | На позицию меню ссылаются исторические заказы |

### Заведения и меню

```mermaid
erDiagram
    VENUES ||--o{ VENUE_API_KEYS : "аутентифицируется"
    VENUES ||--o{ VENUE_WORK_HOURS : "работает по"
    VENUES ||--o| VENUE_WEBHOOKS : "принимает заказы на"
    VENUES ||--o{ MENU_CATEGORIES : "разделы"
    VENUES ||--o{ MENU_ITEMS : "позиции"
    VENUES ||--o{ MENU_IMPORTS : "аудит синхронизаций"
    MENU_CATEGORIES ||--o{ MENU_ITEMS : "содержит"
    MENU_ITEMS ||--o{ MENU_OPTION_GROUPS : "модификаторы"
    MENU_OPTION_GROUPS ||--o{ MENU_OPTIONS : "варианты"

    VENUES {
        uuid id PK
        text slug UK
        text name
        text address_text
        double lat
        double lon
        text timezone "Europe/Moscow"
        text status "active|suspended|draft"
        bool is_accepting_orders "пауза кухни"
        bigint min_order_amount_minor
        bigint delivery_fee_minor
        int delivery_radius_m
        int avg_prep_minutes
        timestamptz created_at
        timestamptz updated_at
    }
    VENUE_API_KEYS {
        uuid id PK
        uuid venue_id FK
        text name "подпись ключа: demo, prod"
        text key_prefix "для поиска"
        text key_hash "sha256, не сам ключ"
        timestamptz created_at
        timestamptz revoked_at
    }
    VENUE_WORK_HOURS {
        uuid id PK
        uuid venue_id FK
        smallint weekday "0=Пн"
        time opens_at
        time closes_at "меньше opens_at = через полночь"
    }
    VENUE_WEBHOOKS {
        uuid venue_id PK
        text url
        text secret "для HMAC"
        bool is_active
        timestamptz created_at
        timestamptz updated_at
    }
    MENU_CATEGORIES {
        uuid id PK
        uuid venue_id FK
        text external_id "ID в системе заведения"
        text name
        int position
        timestamptz created_at
        timestamptz updated_at
    }
    MENU_ITEMS {
        uuid id PK
        uuid venue_id FK
        uuid category_id FK
        text external_id "UK вместе с venue_id"
        text name
        text description
        bigint price_minor
        int weight_grams
        int position "порядок в категории"
        bool is_available "стоп-лист"
        bool is_archived "исчезло из меню"
        timestamptz created_at
        timestamptz updated_at
    }
    MENU_OPTION_GROUPS {
        uuid id PK
        uuid item_id FK
        text external_id
        text name "Размер, Добавки"
        int min_choices
        int max_choices
        int position "порядок показа"
        timestamptz created_at
        timestamptz updated_at
    }
    MENU_OPTIONS {
        uuid id PK
        uuid group_id FK
        text external_id
        text name
        bigint price_delta_minor "может быть отрицательной"
        int position
        bool is_available
        timestamptz created_at
        timestamptz updated_at
    }
    MENU_IMPORTS {
        uuid id PK
        uuid venue_id FK
        text payload_hash "идемпотентность"
        int items_created
        int items_updated
        int items_archived
        timestamptz started_at
        timestamptz finished_at
    }
```

**Ключевое решение — как связать данные платформы с данными заведения.**
`UNIQUE(venue_id, external_id)`. Заведение оперирует **своими** идентификаторами
(артикул из своей 1С/iiko), платформа — своими `uuid`. Связь — составной естественный ключ.
Это значит:

- заведение может слать импорт, не зная про UUID платформы;
- повторный импорт того же снапшота — обновление, а не дубли;
- если заведение переедет на другую CRM и сменит артикулы, мы это увидим как
  «архивировали 24 позиции, создали 24 позиции» в `menu_imports`, а не как тихую порчу данных.

### Корзина и заказы

```mermaid
erDiagram
    USERS ||--o| CARTS : "одна активная"
    USERS ||--o{ ORDERS : "оформляет"
    CARTS ||--o{ CART_ITEMS : ""
    VENUES ||--o{ ORDERS : "исполняет"
    ORDERS ||--|{ ORDER_ITEMS : "снапшот состава"
    ORDERS ||--o{ ORDER_STATUS_HISTORY : "аудит"
    ORDERS ||--o| PAYMENTS : "холд/списание"
    MENU_ITEMS ||--o{ CART_ITEMS : ""
    MENU_ITEMS ||--o{ ORDER_ITEMS : ""

    USERS {
        uuid id PK
        text phone UK
        text name
        timestamptz created_at
    }
    CARTS {
        uuid id PK
        uuid user_id FK "UK: одна корзина"
        uuid venue_id FK "корзина = одно заведение, NULL у пустой"
        timestamptz created_at
        timestamptz updated_at
    }
    CART_ITEMS {
        uuid id PK
        uuid cart_id FK
        uuid menu_item_id FK
        int qty
        jsonb options "выбранные модификаторы"
        bigint added_price_minor "для детекта PRICE_CHANGED"
        timestamptz created_at
        timestamptz updated_at
    }
    ORDERS {
        uuid id PK
        text number UK "К-240915-0042, человекочитаемый"
        uuid user_id FK
        uuid venue_id FK
        text status
        bigint items_amount_minor
        bigint delivery_fee_minor
        bigint total_amount_minor
        text delivery_address
        double delivery_lat
        double delivery_lon
        text contact_phone
        text comment "«позвонить за 10 минут»"
        int eta_minutes
        text idempotency_key "UK вместе с user_id"
        text workflow_id "связь с Temporal"
        text cancel_reason
        timestamptz created_at
        timestamptz updated_at
        timestamptz placed_at
        timestamptz completed_at
    }
    ORDER_ITEMS {
        uuid id PK
        uuid order_id FK
        uuid menu_item_id FK "может быть archived позже"
        text external_item_id "чтобы POS понял"
        text name_snapshot "название на момент заказа"
        bigint price_minor_snapshot "цена на момент заказа"
        int qty
        jsonb options_snapshot
        bigint total_minor
    }
    ORDER_STATUS_HISTORY {
        uuid id PK
        uuid order_id FK
        text from_status
        text to_status
        text actor_type "user|venue|system"
        text reason
        timestamptz created_at
    }
    PAYMENTS {
        uuid id PK
        uuid order_id FK "UK: один платёж на заказ"
        text status "authorized|captured|voided"
        bigint amount_minor
        text provider_ref
        timestamptz created_at
        timestamptz updated_at
    }
```

**Почему `order_items` — снапшот, а не ссылка.** Заведение через час поднимет цену или
архивирует позицию. Чек уже выданного заказа меняться не должен — ни для пользователя,
ни для взаиморасчётов. Ссылка `menu_item_id` остаётся для аналитики, но все отображаемые
и денежные поля скопированы в момент оформления. То же и с `external_item_id`: POS должен
получить заказ в своих артикулах, даже если позицию потом удалили.

### Инфраструктурные таблицы и проекции

```mermaid
erDiagram
    OUTBOX {
        bigint id PK "bigserial; про порядок — см. §5"
        uuid event_id UK "для дедупликации у консьюмера"
        text aggregate_type "order|menu_item|menu_option|venue"
        uuid aggregate_id "= ключ партиции Kafka"
        text event_type "order.placed"
        jsonb payload
        jsonb headers "trace_id"
        timestamptz created_at
        timestamptz published_at "NULL = не отправлено"
        int attempts
        text last_error
    }
    PROCESSED_EVENTS {
        text consumer PK "search-indexer"
        uuid event_id PK
        timestamptz processed_at
    }
    SEARCH_ITEMS {
        uuid item_id PK
        uuid venue_id
        text item_name
        text description "вес C в search_vector"
        text venue_name
        text category_name "для выдачи"
        bigint price_minor
        bool is_available
        bool venue_is_active
        bool venue_is_accepting "пауза приёма (M7)"
        double lat
        double lon
        timestamptz updated_at
        tsvector search_vector "GENERATED, GIN-индекс"
    }
    VENUE_DAILY_STATS {
        uuid venue_id PK
        date stat_date PK "день в часовом поясе заведения"
        int orders_total
        int orders_completed
        int orders_rejected
        int orders_expired
        bigint revenue_minor "по завершённым заказам"
        bigint accept_seconds_total "слагаемое среднего"
        int accept_count "делитель среднего"
        timestamptz updated_at
    }
```

`venue_daily_stats` — проекция `order-events` для сценария M8. Среднее время
подтверждения хранится суммой и счётчиком, а не готовым средним: среднее нельзя
досчитать инкрементально, а сумму и счётчик — можно. День считается в часовом
поясе заведения и там, где витрина наполняется, и там, где читается: «сегодня»
для владельца заведения — это его сегодня.

Индексы, которые действительно нужны:

```sql
-- outbox: только неопубликованные, в порядке записи
CREATE INDEX idx_outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- поиск: полнотекстовый + опечатки
CREATE INDEX idx_search_vector ON search_items USING GIN (search_vector);
CREATE INDEX idx_search_trgm   ON search_items USING GIN (item_name gin_trgm_ops);

-- лента заказов заведения (поллинг-фолбэк)
CREATE INDEX idx_orders_venue_status ON orders (venue_id, status, created_at DESC);

-- меню заведения
CREATE INDEX idx_items_venue_active ON menu_items (venue_id, category_id, position)
    WHERE NOT is_archived;
```

Перечислены индексы, за которыми стоит отдельное решение. Кроме них в схеме
есть обычные индексы по внешним ключам частых выборок — состав корзины и заказа,
история статусов, расписание и категории заведения, лента заказов пользователя,
поиск позиции по заведению в витрине. Уникальность `(venue_id, external_id)`,
`(user_id, idempotency_key)` и `outbox.event_id` обеспечена ограничениями,
отдельные индексы для них не нужны.

Вставка в `outbox` шлёт `NOTIFY outbox_new` триггером на уровне выражения —
relay просыпается на записи, а не ждёт следующего тика опроса. Опрос остаётся
страховкой: уведомление не переживает разрыв соединения, строка в таблице
переживает.

### Индексы, за которыми стоит решение

```sql
-- outbox: только неопубликованные, в порядке записи
CREATE INDEX idx_outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- поиск: полнотекстовый + опечатки
CREATE INDEX idx_search_vector ON search_items USING GIN (search_vector);
CREATE INDEX idx_search_trgm   ON search_items USING GIN (item_name gin_trgm_ops);

-- лента заказов заведения (поллинг-фолбэк)
CREATE INDEX idx_orders_venue_status ON orders (venue_id, status, created_at DESC);

-- меню заведения
CREATE INDEX idx_items_venue_active ON menu_items (venue_id, category_id, position)
    WHERE NOT is_archived;
```

Кроме них в схеме есть обычные индексы по внешним ключам частых выборок — состав
корзины и заказа, история статусов, расписание и категории заведения, лента заказов
пользователя, поиск позиции по заведению в витрине. Уникальность
`(venue_id, external_id)`, `(user_id, idempotency_key)` и `outbox.event_id`
обеспечена ограничениями, отдельные индексы для них не нужны.

Вставка в `outbox` шлёт `NOTIFY outbox_new` триггером на уровне выражения — relay
просыпается на записи, а не ждёт следующего тика опроса. Опрос остаётся страховкой:
уведомление не переживает разрыв соединения, строка в таблице переживает
([ADR 0002](docs/adr/0002-transactional-outbox.md)).

## Бизнес-сценарии

22 сценария: 14 со стороны покупателя и 8 со стороны заведения. Каждый — шаг
в `scripts/demo.sh` с ассертами на код ответа и поля JSON, каждый «код проблемы» —
отдельная ветка в коде и отдельный тест.

CJM обеих сторон в виде Mermaid-диаграмм — в дизайне:
[покупатель (§2)](docs/DESIGN.md#2-пользовательские-сценарии-cjm-покупателя),
[заведение (§3)](docs/DESIGN.md#3-сценарии-заведения-cjm-заведения).

### Покупатель

| # | Сценарий | Что видит пользователь | Код проблемы |
|---|---|---|---|
| U1 | Сквозной заказ | Статус меняется в реальном времени, семь переходов | — |
| U2 | Товар кончился, пока лежал в корзине | «Хинкали недоступны — убрать?» | `ITEM_UNAVAILABLE` |
| U3 | Заведение закрыто | «Откроется завтра в 10:00» | `VENUE_CLOSED` + `next_open_at` |
| U4 | Заведение на паузе | «Временно не принимает заказы» | `VENUE_NOT_ACCEPTING` |
| U5 | Вне зоны доставки | «Доставляет в радиусе 5 км, вы в 7.2 км» | `OUT_OF_DELIVERY_ZONE` |
| U6 | Не набран минимум | «Добавьте ещё на 240 ₽» | `MIN_ORDER_NOT_MET` + `missing_minor` |
| U7 | Цена выросла | «Цена выросла с 450 до 520 ₽. Продолжить?» | `PRICE_CHANGED`, `severity: confirmable` |
| U8 | Корзина из двух заведений | «В корзине блюда из «Хачапури и вино». Очистить?» | `CART_MIXED_VENUES` (409) |
| U9 | Заведение отклонило заказ | «Не может выполнить: закончились продукты» | — |
| U10 | Заведение не ответило | «Не подтвердило заказ, деньги не списаны» | — |
| U11 | Отмена пользователем | До «готово» можно, после — нет | `ORDER_NOT_CANCELLABLE` (409) |
| U12 | Опечатка в поиске | «хачапур» находит «Хачапури по-аджарски» | — |
| U13 | Недоступный модификатор | «Опция «Большая» недоступна» | `OPTION_UNAVAILABLE` |
| U14 | Холд оплаты не прошёл | «Оплата не прошла» | — |

`/cart/validate` отвечает `200` со списком проблем, а не ошибкой: пользователь
должен узнать о них **до** нажатия «Заказать». `blocking` гасит кнопку,
`confirmable` показывает диалог и снимается флагом `accept_price_changes`.

### Заведение

| # | Сценарий | Механика |
|---|---|---|
| M1 | Онбординг | Платформа выдаёт API-ключ → POS импортирует меню → регистрирует вебхук |
| M2 | Обновление меню | Идемпотентный снапшот: diff → `created`/`updated`/`archived`, аудит в `menu_imports` |
| M3 | Стоп-лист | Батч-переключение доступности позиций и опций, авто-возврат по таймеру |
| M4 | Приём заказа | Вебхук → повар видит → `accept` с ETA → `ready` → `delivered` |
| M5 | Отказ | `reject` с причиной → компенсация холда → уведомление пользователю |
| M6 | POS недоступен | Retry вебхука + поллинг-фолбэк `GET /orders?status=placed`, идемпотентность по `order_id` |
| M7 | Пауза приёма | `is_accepting_orders=false` — заведение остаётся в каталоге, но помечено |
| M8 | Витрина статистики | `GET /venue/stats?date=` — проекция `order-events` в `venue_daily_stats`, по умолчанию сегодня в поясе заведения |

Каждая «трудность» воспроизводится одной командой — сценарными триггерами POS,
см. [Пример заведения](#пример-заведения).

## Контракты

Три спецификации, потому что это три разных контракта с разными владельцами:

| Файл | Реализует | Клиент | Аутентификация |
|---|---|---|---|
| [api/public.openapi.yaml](api/public.openapi.yaml) | платформа | веб-клиент | `X-User-Id` (заглушка вместо JWT) |
| [api/merchant.openapi.yaml](api/merchant.openapi.yaml) | платформа | учётная система заведения | `Authorization: Bearer <api-key>` |
| [api/pos-webhooks.openapi.yaml](api/pos-webhooks.openapi.yaml) | **заведение** | платформа | HMAC-подпись `X-Kitchen-Signature` |

Разработка идёт contract-first: сначала YAML, потом `make generate`, потом код.
Сгенерированное лежит в `internal/gen/` и коммитится — сборка не требует тулинга.
Руками эти файлы не правятся.

```bash
make generate   # oapi-codegen: типы, chi-сервер и клиент для каждой спеки
make lint-api   # vacuum: линтер спек, правила — api/vacuum-ruleset.yaml
```

Тест `internal/gen` сравнивает набор операций в YAML с копией спеки внутри
сгенерированного кода, поэтому забытый `make generate` роняет `make test`,
а не всплывает в рантайме.

## Запуск и окружение

```bash
make up      # поднять стенд и дождаться healthy у всех сервисов
make lint    # golangci-lint (конфиг — .golangci.yml)
make test    # все тесты с детектором гонок
make demo    # прогон бизнес-сценариев
make down    # погасить стенд вместе с томами
```

Отдельно проверяется, что диаграммы этого README живы:

```bash
make check-diagrams   # все Mermaid-блоки через mermaid-cli в контейнере
```

Сломанная диаграмма на GitHub выглядит пустым блоком, а не ошибкой, поэтому
глазами такое не ловится — нужен рендерер.

Миграции и демо-данные `make up` накатывает сам: одноразовый сервис `migrator`
стартует после Postgres, остальные ждут его успешного кода возврата. Отдельно:

```bash
make migrate         # накатить схему (goose, миграции встроены в бинарь)
make migrate-status  # что накачено
make migrate-reset   # откатить всё — проверка down-веток
make seed            # демо-каталог: 3 заведения, 26 позиций, 2 пользователя
```

`.env` не обязателен: у всех переменных в `docker-compose.yml` есть значения
по умолчанию. Переопределения — по образцу из [.env.example](.env.example).

| Сервис | Порт на хосте | Назначение |
|---|---|---|
| `kitchen-api` | 8080 | Public + Merchant API, `/healthz`, `/readyz` |
| `kitchen-worker` | 8081 | Temporal worker, outbox-relay, Kafka-консьюмеры |
| `merchant-pos` | 8090 | Пример заведения: SQLite, автопилот, сценарные триггеры |
| Swagger UI | 8085 | Три OpenAPI-контракта в одном селекторе |
| Temporal UI | 8233 | История workflow'ов заказа |
| Postgres | 55432 | Не 5432: локальная СУБД на дефолтном порту есть почти у всех |
| Kafka | 9094 | Внешний listener для консольных утилит |

## Как это работает

Клиентский контур каталога и поиска — на демо-данных, без предварительной настройки:

```bash
curl 'localhost:8080/api/v1/venues?open_now=true'
curl 'localhost:8080/api/v1/venues?lat=55.7402&lon=37.6280&sort=distance'
curl 'localhost:8080/api/v1/venues/01890000-0001-7000-8000-000000000001/menu'
curl 'localhost:8080/api/v1/search?q=хачапур'          # находит несмотря на опечатку
```

Контур заведений — импорт меню, стоп-листы, настройки и вебхук:

```bash
KEY='Authorization: Bearer kitchen_demo_hachapuri_key'
curl -X POST localhost:8080/api/merchant/v1/menu/import -H "$KEY" -d @menu.json
curl -X POST localhost:8080/api/merchant/v1/menu/availability -H "$KEY" \
     -d '{"items":[{"external_id":"hv-004","is_available":false}]}'
curl -X PUT localhost:8080/api/merchant/v1/venue -H "$KEY" \
     -d '{"is_accepting_orders": false}'          # пауза приёма, сценарий M7
```

Импорт идемпотентен по отпечатку снапшота: повторная отправка того же меню
возвращает нули в счётчиках и `"idempotent": true`. Исчезнувшая из снапшота
позиция архивируется мягко — на неё ссылаются исторические заказы. Стоп-лист
доезжает до выдачи поиска в той же транзакции, поэтому снятое блюдо пропадает
из `/search` сразу.

Корзина и проверка перед оформлением:

```bash
U='X-User-Id: 01890000-0000-7000-8000-000000000001'
curl -X POST localhost:8080/api/v1/cart/items -H "$U" \
     -d '{"item_id":"01890000-0003-7000-8000-000000000101","qty":1,
          "option_ids":["01890000-0005-7000-8000-000000000101"]}'
curl -X POST localhost:8080/api/v1/cart/validate -H "$U" \
     -d '{"delivery":{"address":"Москва, Большая Ордынка, 40","lat":55.7361,"lon":37.6252}}'
```

`/cart/validate` отвечает `200` со списком проблем, а не ошибкой: пользователь
должен узнать о них до нажатия «Заказать». `severity: blocking` гасит кнопку,
`confirmable` — показывает диалог «цена выросла, продолжить?». Корзина требует
`X-User-Id`; каталог и поиск по-прежнему работают анонимно.

Оформление заказа:

```bash
U='X-User-Id: 01890000-0000-7000-8000-000000000001'
curl -X POST localhost:8080/api/v1/orders -H "$U" -H 'Idempotency-Key: my-key-1' \
     -d '{"delivery":{"address":"Москва, Большая Ордынка, 40","lat":55.7361,"lon":37.6252},
          "contact_phone":"+79990000001"}'
curl localhost:8080/api/v1/orders -H "$U"                    # история с таймлайном
curl localhost:8080/api/merchant/v1/orders -H "$KEY"          # лента заведения
```

Повтор с тем же `Idempotency-Key` возвращает **тот же** заказ и `200` вместо
`201` — двойной клик и ретрай сети не создают второй заказ. Блокирующие
проблемы корзины дают `422` с тем же `issues[]`, что и `/cart/validate`.

Жизненный цикл заказа ведёт Temporal:

```bash
curl -X POST localhost:8080/api/merchant/v1/orders/$ID/accept -H "$KEY" -d '{"eta_minutes":35}'
curl -X POST localhost:8080/api/merchant/v1/orders/$ID/ready -H "$KEY"
curl -X POST localhost:8080/api/merchant/v1/orders/$ID/delivered -H "$KEY"
curl -X POST localhost:8080/api/v1/orders/$ID/cancel -H "$U" -d '{"reason":"передумал"}'
curl -N localhost:8080/api/v1/orders/$ID/events -H "$U"     # SSE-поток статусов
```

Хендлеры не пишут статус: они шлют сигнал процессу, а статус проставляет
активность внутри workflow — `orders.status` остаётся проекцией состояния
процесса. Ход заказов виден в Temporal UI на [localhost:8233](http://localhost:8233).

Заведение молчит семь минут — заказ отменяется сам и холд возвращается;
отказ или отмена запускают компенсацию; после `ready` отмена даёт `409`.
Мок платежей отказывает детерминированно: на сумме, оканчивающейся
на 99 копеек (сценарий U14).

## Пример заведения

`merchant-pos` — отдельный сервис в этом же репозитории: со своим хранилищем
(SQLite), своим API и своими артикулами. С платформой он общается только
по контрактам из `api/`, а правило `depguard` превращает эту границу в ошибку
сборки — импортировать внутренние пакеты платформы он не может.

На старте заведение делает то же, что делало бы настоящее: импортирует меню
и регистрирует вебхук. Дальше заказы приходят вебхуком с HMAC-подписью,
а автопилот ведёт их сам — принимает, готовит и отдаёт, поэтому демо
не требует человека, нажимающего кнопки.

```bash
curl localhost:8090/pos/orders                          # экран повара
curl -X POST localhost:8090/pos/orders/$ID/accept       # руками, если автопилот выключен
```

Трудности воспроизводятся сценарными триггерами — каждая одной командой:

```bash
curl -X POST localhost:8090/pos/scenario/reject-next              # заведение откажет (M5)
curl -X POST localhost:8090/pos/scenario/ignore-next              # промолчит → таймаут платформы (U10)
curl -X POST 'localhost:8090/pos/scenario/go-offline?seconds=45'  # недоступно → поллинг-фолбэк (M6)
curl -X POST localhost:8090/pos/scenario/stop-item/hv-006         # снимет позицию (U2)
curl -X POST localhost:8090/pos/scenario/stop-option/hv-001-size-l # снимет модификатор (U13)
curl -X POST localhost:8090/pos/scenario/raise-price/hv-004       # поднимет цену на 15 % (U7)
curl -X POST localhost:8090/pos/scenario/reset-menu               # переподключится: меню + вебхук (M1, M2)
```

Раз в сутки платформа сверяет свой каталог с тем, что отдаёт заведение
по `GET /pos/webhooks/menu` — cron-workflow `MenuReconciliationWorkflow`.
Расхождения не чинятся молча, а пишутся в лог: тихая правка данных
по расписанию прячет проблему интеграции вместо того, чтобы её показать.

## События

Изменения каталога и заказов едут наблюдателям через Kafka, а не пишутся
в проекции напрямую:

```
транзакция (данные + outbox) → LISTEN/NOTIFY → outbox-relay → Kafka → консьюмер
```

Событие пишется в ту же транзакцию, что и данные, поэтому «заказ есть, а факта
о нём никто не получил» не бывает. Топик `catalog-events` компактируемый,
события каталога — полные снимки позиции: витрину поиска можно пересобрать
проигрыванием топика. Консьюмеры дедуплицируют по `event_id` в таблице
`processed_events`, поэтому повторная доставка ничего не ломает.

Проверить, что события переживают аварию брокера:

```bash
docker compose stop kafka
curl -X POST localhost:8080/api/merchant/v1/menu/availability -H "$KEY" \
     -d '{"items":[{"external_id":"hv-006","is_available":false}]}'   # 200, событие в outbox
docker compose start kafka                                            # витрина догоняет сама
```

Расчёт «открыто сейчас» — чистая функция в `internal/catalog`: она знает про работу
через полночь, про воскресенье и про часовой пояс заведения, и покрыта таблицей тестов.
Правило живёт в одном месте: фильтрация и сортировка каталога делаются в Go, а не
дублируются в SQL.

## Демо-данные

Сид кладёт в базу три заведения грузинской кухни, 26 позиций меню с модификаторами,
расписание (в том числе работу через полночь) и двух пользователей. Идентификаторы
фиксированные — на них ссылаются демо-сценарии.

| Что | Значение |
|---|---|
| Пользователь | `01890000-0000-7000-8000-000000000001` — в заголовке `X-User-Id` |
| Заведения | `hachapuri-i-vino`, `supra`, `tone` |
| Ключи Merchant API | `kitchen_demo_hachapuri_key`, `kitchen_demo_supra_key`, `kitchen_demo_tone_key` |

Ключ передаётся как `Authorization: Bearer <key>`; в базе лежит только его
sha256. Заведение видит и меняет исключительно свои данные: `venue_id` берётся
из ключа и подставляется в запросы на уровне репозитория, а не хендлера.

Скрипт идемпотентен: повторный `make seed` обновляет демо-строки, а не плодит их.

## Демо-прогон

Скрипт `scripts/demo.sh` готовит предусловия сам — закрытое расписание для U3,
высокий минимум заказа для U6, комиссию с 99 копейками для U14 — и в конце
возвращает стенд к сидовому состоянию, поэтому его можно гонять подряд.
Отдельные сценарии: `make demo ARGS="U7 M3"`.

Нужны `bash`, `curl` и `jq`, поэтому прогон едет в своём контейнере
(`build/Dockerfile.demo`): ставить пакеты на машину проверяющего ради демо
неправильно, а Docker для стенда и так обязателен. Скрипт запускается
и напрямую с хоста, если эти три программы есть.

Каждый сценарий печатает свои проверки, в конце — счёт. Провал любой проверки
валит прогон ненулевым кодом возврата, поэтому скрипт годится и как e2e-тест
в CI, а не только как демонстрация.

```
M6 · POS был недоступен — заказ не потерялся
  ✓ заведение ушло в офлайн — вебхук не пройдёт — 200
  ✓ заказ оформлен, пока заведение недоступно — 201
  вебхук отбивается, заказ должен подобрать поллинг-фолбэк
  ✓ заказ дошёл до статуса cooking за 19с
  ✓ заказ не потерялся: заведение забрало его поллингом

Итог
  сценарии: 22 пройдено
  проверки: 111 пройдено
```

## Тесты

```bash
make test-unit         # юнит-тесты: без docker, секунды
make test-integration  # репозитории на настоящем Postgres (testcontainers)
make test              # и то, и другое, с детектором гонок
make cover             # покрытие доменных пакетов с проверкой порога
make demo              # 22 бизнес-сценария на живом стенде
```

Пирамида собрана по тому, что чем проверяется:

| Слой | Что проверяет | Чем |
|---|---|---|
| Юнит | Матрицу переходов с правами актора, каскад валидации со всеми восемью кодами, расписание (работа через полночь, воскресенье, чужой пояс), деньги, перевод доменных ошибок в `problem+json` | Обычные тесты, без внешних зависимостей |
| Workflow | Пять веток `OrderWorkflow`, стоп-лист с авто-возвратом, суточную сверку | `testsuite.WorkflowTestSuite` — durable-таймеры проходят за миллисекунды, `time.Sleep` в тестах запрещён |
| Интеграционные | Репозитории, активности процесса, outbox-relay и проекции | Настоящий Postgres в контейнере (`testcontainers-go`), схема и демо-данные — из тех же исходников, что и стенд |
| E2E | 22 бизнес-сценария целиком | `scripts/demo.sh` на поднятом стенде |

Интеграционные тесты не мокают базу намеренно: их предмет — руками написанный
SQL, то есть фильтры мультитенантности, блокировки строк, конфликты уникальных
индексов и границы транзакций. Мок хранилища проверял бы, что мок работает.

Порог покрытия — не строчка в README, а `scripts/check-coverage.sh`: он считает
объединённый профиль юнит- и интеграционных тестов и валится, если хоть один
доменный пакет опустился ниже 70 %. Цифра, за которой никто не следит
автоматически, сползает вниз молча.

```
покрытие доменных пакетов (порог 70%):

  ok   internal/cart               82.4%  (257/312)
  ok   internal/catalog            84.2%  (224/266)
  ok   internal/merchant           77.6%  (448/577)
  ok   internal/order              72.8%  (287/394)
  ok   internal/order/workflow     85.4%  (304/356)
  ok   internal/paging            100.0%  (20/20)
  ok   internal/payments          100.0%  (10/10)
  ok   internal/search             86.2%  (100/116)
  ok   internal/stats              78.3%  (36/46)
```

Инфраструктурная обвязка (`pg`, `logger`, `kafkax`, `httpx`, `config`) и
`merchant-pos` под порог не попадают: гнать их до той же цифры значило бы
писать тесты ради цифры.

## Непрерывная интеграция

`.github/workflows/ci.yml` — пять job'ов на каждый push и pull request:

| Job | Что делает |
|---|---|
| `lint` | golangci-lint, линтер OpenAPI-спек и проверка, что кодогенерация не разошлась со спеками |
| `test` | Юнит-тесты с `-race`, затем порог покрытия на объединённом профиле |
| `build` | Бинари и четыре образа тем же `Dockerfile`, что и стенд |
| `e2e` | `make up` + `make demo` — те же 22 сценария, с укороченным таймером подтверждения |
| `diagrams` | Рендер всех Mermaid-блоков |

Версия Go берётся из `go.mod` — единственный источник, разъехаться негде.

## Осознанные упрощения

Каждое — сознательное решение с понятной ценой, а не «не успел».

| Упрощение | Почему допустимо в MVP | Чего стоит | Путь наружу |
|---|---|---|---|
| **Нет авторизации** — `X-User-Id` в заголовке | Прямо разрешено заданием | Любой может подставить чужой id | Middleware уже кладёт user в context — замена на JWT не трогает хендлеры |
| **Платежи замоканы** | Нет провайдера и PCI-контура | Не проверена реальная механика холдов | Интерфейс `payments.Provider` с 3 методами — подменяется реализацией |
| **Зона доставки — радиус, не полигон** | Реальные зоны требуют PostGIS и данных | Неточность на границе | PostGIS + `ST_Contains`; схема уже хранит координаты |
| **Курьеры замоканы** | Отдельный домен размером с этот проект | Нет реальных ETA | Отдельный сервис + activity в workflow |
| **Одна активная корзина** | Так работает большинство фудтех-сервисов | Нельзя параллельно собирать два заказа | `UNIQUE(user_id)` → `UNIQUE(user_id, venue_id)` |
| **JSON в Kafka без Schema Registry** | Один продюсер, версия в имени топика | Нет валидации совместимости | Schema Registry + protobuf, топик `.v2` |
| **Outbox через polling, не CDC** ([ADR 0002](docs/adr/0002-transactional-outbox.md)) | Debezium — отдельный кластер | Задержка до интервала опроса | `LISTEN/NOTIFY` снижает задержку до мс; далее — Debezium |
| **Postgres FTS вместо ES** ([ADR 0007](docs/adr/0007-postgres-fts-not-elasticsearch.md)) | Достаточно на объёмах MVP | Нет фасетов, сложного ранжирования | Консьюмер `search-indexer` — уже выделенная точка замены |
| **Один инстанс всего** | Демо-стенд | Нет проверки на конкурентность реплик | Сервисы stateless; outbox-relay нужен `FOR UPDATE SKIP LOCKED` |
| **«Открыто сейчас» и расстояние считаются в приложении, не в SQL** | Закрытый пул заведений; правило с работой через полночь и чужими поясами живёт в одной протестированной функции, а не дублируется в SQL | Выборка всех активных заведений на каждый запрос каталога | Материализованный флаг открытости в витрине и KNN-индекс PostGIS для расстояния — обе точки локальны в `internal/catalog` |

## Что дальше при масштабировании

1. **Каталог отделяется от заказов** — разный профиль нагрузки (чтение ×1000 против записи).
   Границы уже проведены по модулям `catalog` / `order`.
2. **`search-indexer` переключается на Elasticsearch** — переигрыванием компактируемого
   топика `catalog-events`, без миграции данных.
3. **Outbox-relay заменяется на Debezium CDC** — снимает нагрузку опроса с основной БД.
4. **Шардирование заказов по `venue_id`** — естественный ключ шардирования, заказы
   разных заведений не связаны.
5. **Доставка и оплата выносятся в отдельные сервисы** — в workflow это уже отдельные
   activities, меняется только их реализация.


## Решения (ADR)

Решения, которые дорого менять, вынесены в [docs/adr/](docs/adr/) — по файлу
на решение, с контекстом, ценой и отвергнутыми альтернативами.

| № | Решение |
|---|---|
| [0001](docs/adr/0001-postgres-kafka-temporal.md) | Postgres — источник истины, Temporal — процесс заказа, Kafka — трансляция фактов |
| [0002](docs/adr/0002-transactional-outbox.md) | Публикация событий только через transactional outbox |
| [0003](docs/adr/0003-money-bigint-minor.md) | Деньги — `bigint` в копейках |
| [0004](docs/adr/0004-enum-text-check.md) | Перечисления — `text` + `CHECK`, а не Postgres `ENUM` |
| [0005](docs/adr/0005-uuid-v7-in-app.md) | Идентификаторы — UUID v7, генерирует приложение |
| [0006](docs/adr/0006-mermaid-not-plantuml.md) | Диаграммы — Mermaid инлайном в markdown |
| [0007](docs/adr/0007-postgres-fts-not-elasticsearch.md) | Поиск — Postgres FTS + `pg_trgm`, не Elasticsearch |
| [0008](docs/adr/0008-order-items-snapshot.md) | Состав заказа — снапшот, а не ссылки на меню |
