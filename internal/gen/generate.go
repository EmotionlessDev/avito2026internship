// Package gen — корень сгенерированного из OpenAPI кода.
//
// Файлы `*.gen.go` в подпакетах руками не правятся: источник истины — спеки
// в `api/`, код пересобирается `make generate` и коммитится в репозиторий,
// чтобы сборка не требовала установленного тулинга.
package gen

//go:generate go tool oapi-codegen -config ../../api/codegen/public.yaml ../../api/public.openapi.yaml
//go:generate go tool oapi-codegen -config ../../api/codegen/merchant.yaml ../../api/merchant.openapi.yaml
//go:generate go tool oapi-codegen -config ../../api/codegen/pos.yaml ../../api/pos-webhooks.openapi.yaml
