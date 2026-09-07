// Package gen — корень сгенерированного из OpenAPI кода.
//
// Файлы `*.gen.go` в подпакетах руками не правятся: источник истины — спеки
// в `api/`, код пересобирается `make generate` и коммитится в репозиторий,
// чтобы сборка не требовала установленного тулинга.
//
// Генератор вызывается через `go run` с полным путём пакета: версия берётся
// из go.mod, где её удерживает tools/tools.go. Так у всех, кто запускает
// генерацию, одна и та же версия инструмента.
package gen

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -config ../../api/codegen/public.yaml ../../api/public.openapi.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -config ../../api/codegen/merchant.yaml ../../api/merchant.openapi.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -config ../../api/codegen/pos.yaml ../../api/pos-webhooks.openapi.yaml
