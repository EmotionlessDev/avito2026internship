# Сигнатуры целей зафиксированы в PLAN.md и не меняются между этапами.

SHELL := /bin/bash
.DEFAULT_GOAL := help

LOCAL_BIN := $(CURDIR)/bin
export PATH := $(LOCAL_BIN):$(PATH)

# Версии инструментов пиним здесь, а не полагаемся на то, что стоит у разработчика:
# CI и локальная машина обязаны видеть одинаковые замечания линтера.
GOLANGCI_LINT_VERSION := v2.13.2
GOLANGCI_LINT := $(LOCAL_BIN)/golangci-lint

.PHONY: help
help: ## Показать список целей
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# --- Стенд -------------------------------------------------------------------

.PHONY: up
up: ## Поднять стенд и дождаться healthy у всех сервисов
	docker compose up -d --build --wait

.PHONY: down
down: ## Погасить стенд вместе с томами
	docker compose down -v --remove-orphans

.PHONY: logs
logs: ## Хвост логов стенда
	docker compose logs -f --tail=100

.PHONY: ps
ps: ## Состояние контейнеров
	docker compose ps

# --- Код ---------------------------------------------------------------------

.PHONY: build
build: ## Собрать все бинари
	go build ./...

.PHONY: test
test: ## Все тесты с детектором гонок (интеграционным нужен docker)
	go test ./... -race -count=1

.PHONY: test-unit
test-unit: ## Только юнит-тесты — без docker и без контейнеров
	go test ./internal/... ./migrations/... -race -count=1

.PHONY: test-integration
test-integration: ## Репозитории на настоящем Postgres (testcontainers)
	go test ./tests/integration/... -count=1

.PHONY: cover
cover: ## Покрытие доменных пакетов с проверкой порога
	./scripts/check-coverage.sh

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Статический анализ (.golangci.yml)
	$(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: $(GOLANGCI_LINT) ## Форматирование (gofumpt + goimports)
	$(GOLANGCI_LINT) fmt ./...

.PHONY: tidy
tidy: ## Привести go.mod/go.sum в порядок
	go mod tidy

$(GOLANGCI_LINT):
	@echo "устанавливаю golangci-lint $(GOLANGCI_LINT_VERSION) в $(LOCAL_BIN)"
	@GOBIN=$(LOCAL_BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# --- Контракты ---------------------------------------------------------------
# Версия oapi-codegen пиньнется директивой tool в go.mod, версия vacuum —
# тегом образа: обе одинаковы у всех, кто трогает контракты.

VACUUM_VERSION := v0.30.3
VACUUM := docker run --rm -v $(CURDIR):/work -w /work dshanley/vacuum:$(VACUUM_VERSION)

.PHONY: generate
generate: ## Кодогенерация из OpenAPI (результат коммитится)
	go generate ./...

.PHONY: lint-api
lint-api: ## Линтер OpenAPI-спек (vacuum, конфиг — api/vacuum-ruleset.yaml)
	$(VACUUM) lint -r api/vacuum-ruleset.yaml -d --fail-severity info \
		api/public.openapi.yaml api/merchant.openapi.yaml api/pos-webhooks.openapi.yaml

# --- База --------------------------------------------------------------------
# Мигратор гоняется тем же образом, что и в стенде: одинаковый бинарь локально
# и в compose исключает «у меня накатилось, а на стенде нет». Postgres compose
# поднимает сам по depends_on.

.PHONY: migrate
migrate: ## Накатить миграции
	docker compose run --rm --build migrator up

.PHONY: migrate-down
migrate-down: ## Откатить последнюю миграцию
	docker compose run --rm --build migrator down

.PHONY: migrate-reset
migrate-reset: ## Откатить все миграции — проверка down-веток
	docker compose run --rm --build migrator reset

.PHONY: migrate-status
migrate-status: ## Показать состояние миграций
	docker compose run --rm --build migrator status

.PHONY: seed
seed: ## Наполнить базу демо-данными
	docker compose run --rm --build migrator seed

.PHONY: check-diagrams
check-diagrams: ## Проверить, что все Mermaid-диаграммы рендерятся
	./scripts/check-diagrams.sh

.PHONY: demo
demo: ## Прогнать бизнес-сценарии (22 шт.); ARGS="U7 M3" — подмножество
	# Без --build намеренно: образ раннера собирается сам при первом запуске,
	# а форсированная пересборка каждый раз ходит в реестр за метаданными
	# alpine и роняет прогон на сетевой икоте.
	docker compose run --rm demo $(ARGS)
