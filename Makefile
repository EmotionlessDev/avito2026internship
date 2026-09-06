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
test: ## Прогнать тесты с детектором гонок
	go test ./... -race -count=1

.PHONY: cover
cover: ## Тесты с отчётом о покрытии
	go test ./... -race -count=1 -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out | tail -1

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

# --- Появляются на следующих этапах ------------------------------------------

.PHONY: generate
generate: ## Кодогенерация из OpenAPI (этап 2)
	go generate ./...

.PHONY: migrate
migrate: ## Накатить миграции (этап 1)
	@echo "migrate: появится на этапе 1 (goose + cmd/migrator)"

.PHONY: seed
seed: ## Наполнить базу демо-данными (этап 1)
	@echo "seed: появится на этапе 1 (scripts/seed)"

.PHONY: demo
demo: ## Прогнать бизнес-сценарии (этап 9)
	@echo "demo: появится на этапе 9 (scripts/demo.sh)"
