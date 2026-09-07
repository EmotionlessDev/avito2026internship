package order

import (
	"fmt"
	"slices"
)

// transition — разрешённый переход и те, кто имеет право его выполнить.
type transition struct {
	to     Status
	actors []Actor
}

// transitions — матрица переходов из docs/DESIGN.md §6.1.
//
// Матрица знает не только «куда можно», но и «кому можно». Это две разные
// ошибки: заведение не может отменить заказ вместо пользователя, а пользователь
// не может объявить заказ готовым. Без прав актора обе попытки выглядели бы
// одинаково допустимыми.
var transitions = map[Status][]transition{
	StatusCreated: {
		{to: StatusPlaced, actors: []Actor{ActorSystem}},
		{to: StatusPaymentFailed, actors: []Actor{ActorSystem}},
	},
	StatusPlaced: {
		{to: StatusAccepted, actors: []Actor{ActorVenue}},
		{to: StatusRejected, actors: []Actor{ActorVenue}},
		// Таймаут подтверждения: заведение молчало семь минут (сценарий U10).
		{to: StatusExpired, actors: []Actor{ActorSystem}},
		{to: StatusCancelled, actors: []Actor{ActorUser}},
	},
	StatusAccepted: {
		// Готовка начинается автоматически по подтверждению.
		{to: StatusCooking, actors: []Actor{ActorSystem}},
		{to: StatusCancelled, actors: []Actor{ActorUser}},
	},
	StatusCooking: {
		{to: StatusReady, actors: []Actor{ActorVenue}},
		{to: StatusCancelled, actors: []Actor{ActorUser}},
	},
	StatusReady: {
		// После готовности отменять поздно: блюда уже приготовлены (U11).
		{to: StatusDelivering, actors: []Actor{ActorSystem}},
	},
	StatusDelivering: {
		{to: StatusCompleted, actors: []Actor{ActorSystem, ActorVenue}},
	},
}

// ErrInvalidTransition — переход не разрешён машиной состояний.
type ErrInvalidTransition struct {
	From  Status
	To    Status
	Actor Actor
}

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("недопустимый переход %s → %s для актора %s", e.From, e.To, e.Actor)
}

// ErrForbiddenActor — переход существует, но этот актор не имеет на него права.
type ErrForbiddenActor struct {
	From  Status
	To    Status
	Actor Actor
}

func (e ErrForbiddenActor) Error() string {
	return fmt.Sprintf("актор %s не может перевести заказ %s → %s", e.Actor, e.From, e.To)
}

// CanTransition проверяет переход и права актора.
//
// Возвращает разные ошибки на «так нельзя никому» и «так нельзя тебе»:
// первое — 409 для клиента, второе — признак того, что вызов пришёл не из
// того контура.
func CanTransition(from, to Status, actor Actor) error {
	allowed, known := transitions[from]
	if !known {
		return ErrInvalidTransition{From: from, To: to, Actor: actor}
	}

	for _, candidate := range allowed {
		if candidate.to != to {
			continue
		}

		if slices.Contains(candidate.actors, actor) {
			return nil
		}

		return ErrForbiddenActor{From: from, To: to, Actor: actor}
	}

	return ErrInvalidTransition{From: from, To: to, Actor: actor}
}

// IsTerminal сообщает, что заказ дальше не поедет.
func IsTerminal(status Status) bool {
	_, hasNext := transitions[status]

	return !hasNext
}

// IsCancellable отвечает, может ли пользователь отменить заказ сейчас.
// Ровно это правило видит пользователь как «до готовности — можно,
// после — нет» (сценарий U11).
func IsCancellable(status Status) bool {
	return CanTransition(status, StatusCancelled, ActorUser) == nil
}
