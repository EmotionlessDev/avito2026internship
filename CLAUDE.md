# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Что это

Тестовое задание «Авито.Кухня»: MVP платформы доставки еды плюс пример заведения
(`merchant-pos`) в одном репозитории. Репозиторий в фазе проектирования — кода ещё нет,
есть три управляющих документа:

- `backend-trainee-assignment-autumn-2026.md` — текст задания и критерии приёмки. Не редактируется.
- `PLAN.md` — этапы 0–11 с DoD. Разработка идёт строго по этапам; каждый этап
  заканчивается работающим состоянием (`docker compose up` поднимается,
  `make lint test` зелёный).
- `docs/DESIGN.md` — сценарии (U1–U14, M1–M8), архитектура, модель данных (19 таблиц),
  состав API, решения с обоснованием.

**Правило синхронизации.** Код не может противоречить `DESIGN.md`. Если при реализации
решение меняется — сначала изменения апрувит пользователь, правится DESIGN (и ADR в `docs/adr/`), потом код.
Связанные счётчики: новый пользовательский сценарий = строка в DESIGN §2 + код проблемы
в `issues[]` + тест + шаг в `demo.sh` + строка в чек-листе PLAN.

## Команды

Появляются на этапе 0 (Makefile); сигнатуры зафиксированы в PLAN и не меняются:

- `make up` / `make down` — Docker Compose: postgres, kafka (KRaft), temporal,
  temporal-ui, swagger-ui, kitchen-api, kitchen-worker, merchant-pos, migrator (one-shot).
- `make lint` — golangci-lint, конфиг `.golangci.yml` в корне.
- `make test` — все тесты; одиночный: `go test ./internal/order/... -run 'TestИмя'`.
- `make generate` — oapi-codegen из `api/*.openapi.yaml`; сгенерированный код коммитится.
- `make migrate` / `make seed` — goose (миграции через embed); сид: 3 заведения,
  ~25 позиций, 2 демо-пользователя.
- `scripts/demo.sh` — 22 сценария (14 U + 8 M) с ассертами; e2e-доказательство
  критерия приёмки «выполняет все бизнес-сценарии».

## Архитектура

Три Go-бинаря в одном модуле (детали — DESIGN §4):

- `cmd/kitchen-api` — HTTP (chi), контуры public + merchant; состояние читает
  **только из Postgres**.
- `cmd/kitchen-worker` — Temporal worker, Kafka-консьюмеры (`search-indexer`,
  `notifier`, `stats`), outbox-relay.
- `cmd/merchant-pos` — эмулятор заведения: собственный SQLite, общается с платформой
  **только по HTTP-контрактам** из `api/`; импорт `internal/*`-пакетов платформы
  (кроме сгенерированных клиентов) запрещён — это отдельная система.

Главный ADR (DESIGN §5), не нарушать:

> Postgres — источник истины (все чтения). Temporal — процесс одного заказа
> (таймеры, сигналы, компенсации). Kafka — трансляция фактов наблюдателям.

Поток записи заказа: одна транзакция (`orders` + снапшот `order_items` + `outbox`) →
outbox-relay (`FOR UPDATE SKIP LOCKED` + `LISTEN/NOTIFY`) → Kafka / Temporal.
`orders.status` — проекция workflow; пишут её только activities, читает HTTP API.

Три OpenAPI-контракта в `api/`: `public`, `merchant`, `pos-webhooks`
(последний реализует заведение, платформа для него — клиент).

## Правила разработки

- **Contract-first.** Сначала `api/*.openapi.yaml` и `migrations/`, затем
  `make generate`, затем код. Сгенерированное руками не редактировать.
- **pgx/v5 без ORM**, SQL пишется руками (обоснование — PLAN «Почему не ORM»).
- **Деньги** — `bigint` в копейках (`*_minor`); float/numeric в арифметике запрещены.
- **id** — UUID v7, генерирует приложение (`google/uuid`); `uuid-ossp` не подключать.
- **Enum** — `text + CHECK`, не Postgres ENUM. **Время** — `timestamptz` в UTC;
  таймзона заведения применяется только при расчёте расписания.
  **Удаление** — мягкое (`is_archived`).
- **Мультитенантность.** `venue_id` из API-ключа кладётся middleware в context
  и подставляется в `WHERE` на уровне репозитория, не хендлера.
  Чужой ресурс — 404, не 403.
- **Temporal-детерминизм.** В коде workflow нет `time.Now()`, `rand`, HTTP и SQL —
  только `workflow.Now()`, `workflow.NewTimer`, activities. Изменения workflow —
  через `workflow.GetVersion`. Все activities идемпотентны.
- **События.** Публикация только через outbox в транзакции с данными; события
  каталога — полные снапшоты позиции (fat events, топик компактируемый); консьюмеры
  дедуплицируют по `event_id` в `processed_events` в транзакции проекции.
- **Ошибки HTTP** — RFC 9457 `problem+json` с полем `code`. Ответ `/cart/validate` —
  `200` с `issues[]`, это диагноз, а не ошибка.
- **Идемпотентность оформления** — `Idempotency-Key` +
  `UNIQUE(user_id, idempotency_key)`; повтор возвращает тот же заказ и `200`.
- **Моки детерминированы.** Отказ холда — сумма заказа, оканчивающаяся на 99 копеек
  (сценарий U14). Никакого `rand` в моках: демо и e2e должны быть воспроизводимы.
- **Миграции goose** — up **и** down в каждом файле, откат проверяется.

## Правила тестирования

Пирамида из PLAN (этапы 3–11):

- **Юнит** (без внешних зависимостей): матрица FSM с проверкой прав актора
  (`user|venue|system`), каскад валидации корзины — все 8 кодов, расчёт «открыто
  сейчас» (работа через полночь, воскресенье, чужой часовой пояс), деньги.
- **Workflow**: `testsuite.WorkflowTestSuite`; пять веток `OrderWorkflow` — accept,
  reject, таймаут подтверждения, отмена пользователем, отказ холда. Durable-таймеры
  проходят за миллисекунды; `time.Sleep` в тестах запрещён.
- **Интеграционные**: репозитории на реальном Postgres через testcontainers-go
  (`tests/integration`).
- **E2E** (`tests/e2e`, `scripts/demo.sh`): happy path и «трудности» через сценарные
  триггеры POS — `reject-next`, `ignore-next`, `go-offline`, `stop-item`,
  `stop-option`, `raise-price`.
- **Пороги**: покрытие доменных пакетов ≥ 70 %; `make lint test` зелёный локально
  и в CI (GitHub Actions: lint + test + build).
- Перед закрытием этапа — прогнать его **DoD из PLAN.md** дословно.

## Документация

- «У каждой диаграммы один дом»: C4 и ERD после этапа 10 живут в README,
  CJM — в DESIGN §2–3. Копий не заводить — ставить ссылку.
- Все диаграммы — Mermaid инлайном в markdown (обоснование — PLAN, этап 10);
  PlantUML не использовать.
- `README.md` — пока стаб; наполняется по этапам, разделы-заглушки не удалять.
