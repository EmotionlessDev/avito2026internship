#!/usr/bin/env bash
#
# demo.sh — прогон бизнес-сценариев «Авито.Кухни» по docs/DESIGN.md §2–3.
#
# Это не набор curl'ов для чтения глазами, а тест: каждый шаг проверяет код
# ответа и поля JSON, а расхождение валит прогон. Скрипт — доказательство
# критерия приёмки «приложение выполняет все бизнес-сценарии».
#
# Предусловия скрипт готовит сам через Merchant API и сценарные ручки POS,
# а в конце возвращает стенд к сидовому состоянию, поэтому прогон повторяем.
#
# Использование:
#   scripts/demo.sh              # все 22 сценария
#   scripts/demo.sh U7 M3        # только перечисленные
#
# Переменные окружения:
#   API_BASE   (http://localhost:8080)  адрес платформы
#   POS_BASE   (http://localhost:8090)  адрес заведения
#   VENUE_KEY  ключ Merchant API заведения «Хачапури и вино»
#   USER_ID    демо-покупатель из сида
#   ORDER_CONFIRMATION_TIMEOUT (7m) — тот же, что у kitchen-api; из него
#              считается бюджет ожидания в сценарии U10
#   NO_COLOR   любое значение выключает цвет

set -uo pipefail

API_BASE=${API_BASE:-http://localhost:8080}
POS_BASE=${POS_BASE:-http://localhost:8090}
VENUE_KEY=${VENUE_KEY:-kitchen_demo_hachapuri_key}
USER_ID=${USER_ID:-01890000-0000-7000-8000-000000000001}
ORDER_CONFIRMATION_TIMEOUT=${ORDER_CONFIRMATION_TIMEOUT:-7m}

# Идентификаторы фиксированы сидом (scripts/seed/data.sql): демо ссылается
# на них по значению, потому что воспроизводимость важнее красоты uuid.
VENUE_1=01890000-0001-7000-8000-000000000001   # Хачапури и вино
HV_001=01890000-0003-7000-8000-000000000101    # Хачапури по-аджарски, 520 ₽
HV_002=01890000-0003-7000-8000-000000000102    # Хачапури по-имеретински, 450 ₽
HV_004=01890000-0003-7000-8000-000000000104    # Хинкали с бараниной, 390 ₽
HV_005=01890000-0003-7000-8000-000000000105    # Хинкали с говядиной, 360 ₽
HV_006=01890000-0003-7000-8000-000000000106    # Хинкали с сыром, 340 ₽
SIZE_L=01890000-0005-7000-8000-000000000102    # модификатор «Большая»
TN_002=01890000-0003-7000-8000-000000000302    # Хачапури-пеновани — другое заведение

# Сидовые настройки заведения: к ним возвращаемся в конце прогона.
SEED_MIN_ORDER=100000
SEED_DELIVERY_FEE=14900
SEED_RADIUS=5000
SEED_PREP=30

ADDRESS='Москва, Пятницкая улица, 20'
LAT=55.7361
LON=37.6252
PHONE='+79990000001'

# --- вывод -----------------------------------------------------------------

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'
    C_GREEN=$'\033[32m'; C_RED=$'\033[31m'; C_YELLOW=$'\033[33m'; C_BLUE=$'\033[34m'
else
    C_RESET=''; C_BOLD=''; C_DIM=''; C_GREEN=''; C_RED=''; C_YELLOW=''; C_BLUE=''
fi

CHECKS_PASSED=0
CHECKS_FAILED=0
SCENARIOS_PASSED=0
SCENARIOS_FAILED=0
FAILED_LIST=''
CURRENT_FAILED=0

say()  { printf '%s\n' "$*"; }
head1() { printf '\n%s%s%s\n' "$C_BOLD$C_BLUE" "$*" "$C_RESET"; }
note() { printf '  %s%s%s\n' "$C_DIM" "$*" "$C_RESET"; }

ok() {
    CHECKS_PASSED=$((CHECKS_PASSED + 1))
    printf '  %s✓%s %s\n' "$C_GREEN" "$C_RESET" "$1"
}

bad() {
    CHECKS_FAILED=$((CHECKS_FAILED + 1))
    CURRENT_FAILED=$((CURRENT_FAILED + 1))
    printf '  %s✗%s %s\n' "$C_RED" "$C_RESET" "$1"
    printf '    %sответ: %s%s\n' "$C_DIM" "$(printf '%s' "$HTTP_BODY" | head -c 400)" "$C_RESET"
}

die() { printf '%s%s%s\n' "$C_RED" "$*" "$C_RESET" >&2; exit 1; }

# --- HTTP ------------------------------------------------------------------

HTTP_STATUS=''
HTTP_BODY=''

_req() {
    local method=$1 url=$2 body=$3
    shift 3

    local tmp
    tmp=$(mktemp)

    if [ -n "$body" ]; then
        HTTP_STATUS=$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" "$url" \
            -H 'Content-Type: application/json' --data "$body" "$@")
    else
        HTTP_STATUS=$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" "$url" "$@")
    fi

    HTTP_BODY=$(cat "$tmp")
    rm -f "$tmp"
}

pub() { _req "$1" "$API_BASE/api/v1$2" "${3:-}" -H "X-User-Id: $USER_ID" "${@:4}"; }
mer() { _req "$1" "$API_BASE/api/merchant/v1$2" "${3:-}" -H "Authorization: Bearer $VENUE_KEY" "${@:4}"; }
pos() { _req "$1" "$POS_BASE$2" "${3:-}" "${@:4}"; }

# --- ассерты ---------------------------------------------------------------

assert_status() {
    local want=$1 what=$2
    if [ "$HTTP_STATUS" = "$want" ]; then
        ok "$what — $want"
    else
        bad "$what — ждали $want, получили $HTTP_STATUS"
    fi
}

# assert_json <jq-фильтр> <ожидаемое> <описание>
assert_json() {
    local filter=$1 want=$2 what=$3 got
    got=$(printf '%s' "$HTTP_BODY" | jq -r "$filter" 2>/dev/null)

    if [ "$got" = "$want" ]; then
        ok "$what = $want"
    else
        bad "$what — ждали $want, получили ${got:-<пусто>}"
    fi
}

# assert_true <jq-фильтр> <описание> — фильтр должен вернуть true
assert_true() {
    local filter=$1 what=$2
    if printf '%s' "$HTTP_BODY" | jq -e "$filter" >/dev/null 2>&1; then
        ok "$what"
    else
        bad "$what"
    fi
}

assert_issue() {
    local code=$1 severity=$2
    assert_true "[.issues[] | select(.code == \"$code\" and .severity == \"$severity\")] | length == 1" \
        "issues[] содержит $code ($severity)"
}

# --- время -----------------------------------------------------------------

# duration_seconds переводит go-подобную длительность (7m, 25s, 1m30s) в секунды.
duration_seconds() {
    printf '%s' "$1" | awk '
        {
            total = 0
            while (match($0, /[0-9]+[hms]/)) {
                part = substr($0, RSTART, RLENGTH)
                unit = substr(part, length(part), 1)
                value = substr(part, 1, length(part) - 1) + 0
                if (unit == "h") total += value * 3600
                else if (unit == "m") total += value * 60
                else total += value
                $0 = substr($0, RSTART + RLENGTH)
            }
            print total
        }'
}

# await_status <order_id> <статусы через пробел> <бюджет секунд>
await_status() {
    local order_id=$1 wanted=$2 budget=$3 elapsed=0 status want

    while [ "$elapsed" -lt "$budget" ]; do
        pub GET "/orders/$order_id"
        status=$(printf '%s' "$HTTP_BODY" | jq -r '.status // empty' 2>/dev/null)

        for want in $wanted; do
            if [ "$status" = "$want" ]; then
                ok "заказ дошёл до статуса $status за ${elapsed}с"

                return 0
            fi
        done

        sleep 1
        elapsed=$((elapsed + 1))
    done

    bad "заказ не дошёл до «$wanted» за ${budget}с, застрял в «${status:-?}»"

    return 1
}

# await_price <external_id> <ожидаемая цена> — ждём, пока событие каталога доедет
await_price() {
    local external=$1 want=$2 elapsed=0 got

    while [ "$elapsed" -lt 20 ]; do
        pub GET "/venues/$VENUE_1/menu"
        got=$(printf '%s' "$HTTP_BODY" |
            jq -r --arg id "$external" '.categories[].items[] | select(.external_id == $id) | .price_minor' 2>/dev/null)

        [ "$got" = "$want" ] && return 0

        sleep 1
        elapsed=$((elapsed + 1))
    done

    return 1
}

# await_availability <external_id> <true|false>
await_availability() {
    local external=$1 want=$2 elapsed=0 got

    while [ "$elapsed" -lt 20 ]; do
        pub GET "/venues/$VENUE_1/menu?include_unavailable=true"
        got=$(printf '%s' "$HTTP_BODY" |
            jq -r --arg id "$external" '.categories[].items[] | select(.external_id == $id) | .is_available' 2>/dev/null)

        [ "$got" = "$want" ] && return 0

        sleep 1
        elapsed=$((elapsed + 1))
    done

    return 1
}

# await_pos_idle ждёт, пока у заведения не останется заказов в работе.
#
# Триггеры reject-next и ignore-next одноразовые и срабатывают на ближайшем
# решении автопилота. Если у заведения ещё крутится заказ предыдущего
# сценария, триггер достанется ему — и прогон станет плавающим.
await_pos_idle() {
    local elapsed=0 active

    while [ "$elapsed" -lt 60 ]; do
        pos GET '/pos/orders'
        active=$(printf '%s' "$HTTP_BODY" | jq -r --arg since "$RUN_STARTED_AT" '
            [.items[]
             | select(.received_at >= $since)
             | select(.status == "new" or .status == "accepted" or .status == "ready")]
            | length' 2>/dev/null)

        [ "$active" = 0 ] && return 0

        sleep 1
        elapsed=$((elapsed + 1))
    done

    return 1
}

# --- корзина и заказы ------------------------------------------------------

cart_reset() { pub DELETE '/cart' >/dev/null 2>&1 || true; }

# cart_add <item_id> <qty> [option_id]
cart_add() {
    local item=$1 qty=$2 option=${3:-}

    if [ -n "$option" ]; then
        pub POST '/cart/items' "{\"item_id\":\"$item\",\"qty\":$qty,\"option_ids\":[\"$option\"]}"
    else
        pub POST '/cart/items' "{\"item_id\":\"$item\",\"qty\":$qty}"
    fi
}

order_body() {
    local extra=${1:-}

    printf '{"delivery":{"address":"%s","lat":%s,"lon":%s},"contact_phone":"%s"%s}' \
        "$ADDRESS" "$LAT" "$LON" "$PHONE" "$extra"
}

# place_order <idempotency-key> [доп. поля JSON, начиная с запятой]
place_order() {
    local key=$1 extra=${2:-}

    pub POST '/orders' "$(order_body "$extra")" -H "Idempotency-Key: $key"
}

order_id_from_body() { printf '%s' "$HTTP_BODY" | jq -r '.id // empty'; }

idem_key() { printf 'demo-%s-%s' "$RUN_ID" "$1"; }

validate_cart() {
    local body=${1:-}
    [ -n "$body" ] || body='{}'

    pub POST '/cart/validate' "$body"
}

# --- предусловия и уборка --------------------------------------------------

all_day_hours() {
    local weekday out=''

    for weekday in 0 1 2 3 4 5 6; do
        out="$out{\"weekday\":$weekday,\"opens_at\":\"00:00\",\"closes_at\":\"23:59\"},"
    done

    printf '%s' "${out%,}"
}

seed_hours() {
    local weekday out=''

    for weekday in 0 1 2 3 4 5 6; do
        out="$out{\"weekday\":$weekday,\"opens_at\":\"10:00\",\"closes_at\":\"23:00\"},"
    done

    printf '%s' "${out%,}"
}

# closed_hours отдаёт расписание, заведомо закрытое прямо сейчас: окно
# сдвинуто на два часа вперёд по часовому поясу заведения (Europe/Moscow).
# Считаем от UTC, а не от локального времени машины, — прогон не должен
# зависеть от того, где стоит ноутбук.
closed_hours() {
    local msk_hour opens closes weekday out=''

    msk_hour=$(( (10#$(date -u +%H) + 3) % 24 ))
    opens=$(( (msk_hour + 2) % 24 ))
    closes=$(( (msk_hour + 3) % 24 ))

    for weekday in 0 1 2 3 4 5 6; do
        out="$out$(printf '{"weekday":%d,"opens_at":"%02d:00","closes_at":"%02d:00"},' \
            "$weekday" "$opens" "$closes")"
    done

    printf '%s' "${out%,}"
}

venue_settings() {
    local hours=$1 min_order=$2 fee=$3

    mer PUT '/venue' "{\"is_accepting_orders\":true,\"min_order_amount_minor\":$min_order,\
\"delivery_fee_minor\":$fee,\"delivery_radius_m\":$SEED_RADIUS,\"avg_prep_minutes\":$SEED_PREP,\
\"timezone\":\"Europe/Moscow\",\"work_hours\":[$hours]}"
}

restore_availability() {
    mer POST '/menu/availability' '{
      "items": [
        {"external_id":"hv-001","is_available":true},{"external_id":"hv-002","is_available":true},
        {"external_id":"hv-003","is_available":true},{"external_id":"hv-004","is_available":true},
        {"external_id":"hv-005","is_available":true},{"external_id":"hv-006","is_available":true},
        {"external_id":"hv-007","is_available":true},{"external_id":"hv-008","is_available":true},
        {"external_id":"hv-009","is_available":true}
      ],
      "options": [
        {"external_id":"hv-001-size-m","is_available":true},
        {"external_id":"hv-001-size-l","is_available":true},
        {"external_id":"hv-001-extra-cheese","is_available":true},
        {"external_id":"hv-001-extra-egg","is_available":true},
        {"external_id":"hv-001-extra-butter","is_available":true}
      ]
    }'
}

setup() {
    head1 'Подготовка стенда'

    venue_settings "$(all_day_hours)" "$SEED_MIN_ORDER" "$SEED_DELIVERY_FEE"
    [ "$HTTP_STATUS" = 200 ] || die "не удалось выставить настройки заведения (HTTP $HTTP_STATUS)"

    # Меню и вебхук — в исходное состояние: прогон не должен зависеть
    # от того, чем закончился предыдущий.
    pos POST '/pos/scenario/reset-menu'
    [ "$HTTP_STATUS" = 200 ] || die "заведение не переподключилось к платформе (HTTP $HTTP_STATUS)"

    restore_availability >/dev/null 2>&1
    cart_reset

    note "заведение открыто круглосуточно на время прогона, минимум $SEED_MIN_ORDER коп."
    note "меню заведения сброшено, вебхук перерегистрирован"
}

teardown() {
    head1 'Возврат стенда к сидовому состоянию'

    venue_settings "$(seed_hours)" "$SEED_MIN_ORDER" "$SEED_DELIVERY_FEE" >/dev/null 2>&1
    restore_availability >/dev/null 2>&1
    pos POST '/pos/scenario/reset-menu' >/dev/null 2>&1
    cart_reset

    note 'расписание 10:00–23:00, стоп-листы сняты, цены исходные'
}

# --- пользовательские сценарии ---------------------------------------------

scenario_U1() {
    cart_reset
    cart_add "$HV_004" 3
    assert_status 200 'позиция добавлена в корзину'

    place_order "$(idem_key u1)"
    assert_status 201 'заказ оформлен'
    assert_json '.status' 'created' 'статус нового заказа'

    local order_id
    order_id=$(order_id_from_body)

    await_status "$order_id" 'completed' 180 || return 1

    assert_true '[.timeline[].to_status] == ["created","placed","accepted","cooking","ready","delivering","completed"]' \
        'таймлайн прошёл все семь переходов'
    assert_json '.items_amount_minor' '117000' 'сумма блюд'
    assert_json '.total_amount_minor' '131900' 'сумма с доставкой'

    # Повтор с тем же ключом обязан вернуть тот же заказ и 200, а не 201.
    place_order "$(idem_key u1)"
    assert_status 200 'повтор с тем же Idempotency-Key'
    assert_json '.id' "$order_id" 'повтор вернул тот же заказ'
}

scenario_U2() {
    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    pos POST '/pos/scenario/stop-item/hv-004'
    assert_status 200 'заведение сняло позицию с продажи'

    if await_availability hv-004 false; then
        ok 'стоп-лист доехал до витрины'
    else
        bad 'стоп-лист не доехал до витрины за 20с'
    fi

    validate_cart
    assert_status 200 'валидация корзины'
    assert_issue ITEM_UNAVAILABLE blocking
    assert_json '.is_valid' 'false' 'корзину нельзя оформить'

    restore_availability >/dev/null
}

scenario_U3() {
    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    venue_settings "$(closed_hours)" "$SEED_MIN_ORDER" "$SEED_DELIVERY_FEE"
    assert_status 200 'заведению выставлено закрытое расписание'

    validate_cart
    assert_issue VENUE_CLOSED blocking
    assert_true '[.issues[] | select(.code == "VENUE_CLOSED") | .next_open_at] | length == 1' \
        'в проблеме есть next_open_at'

    venue_settings "$(all_day_hours)" "$SEED_MIN_ORDER" "$SEED_DELIVERY_FEE" >/dev/null
}

scenario_U4() {
    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    mer PUT '/venue' '{"is_accepting_orders":false}'
    assert_status 200 'заведение поставило приём на паузу'

    validate_cart
    assert_issue VENUE_NOT_ACCEPTING blocking
    assert_json '.is_valid' 'false' 'корзину нельзя оформить'

    mer PUT '/venue' '{"is_accepting_orders":true}' >/dev/null
}

scenario_U5() {
    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    # Точка в Зеленограде — заведомо дальше 5 км от Пятницкой.
    validate_cart '{"delivery":{"address":"Зеленоград, Центральный проспект, 1","lat":55.9825,"lon":37.1814}}'
    assert_status 200 'валидация с дальним адресом'
    assert_issue OUT_OF_DELIVERY_ZONE blocking
    assert_true '[.issues[] | select(.code == "OUT_OF_DELIVERY_ZONE") | select(.distance_m > .delivery_radius_m)] | length == 1' \
        'расстояние больше радиуса доставки'
}

scenario_U6() {
    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    venue_settings "$(all_day_hours)" 500000 "$SEED_DELIVERY_FEE"
    assert_status 200 'заведение подняло минимальную сумму заказа'

    validate_cart
    assert_issue MIN_ORDER_NOT_MET blocking
    assert_true '[.issues[] | select(.code == "MIN_ORDER_NOT_MET") | select(.min_order_minor == 500000 and .missing_minor == 383000)] | length == 1' \
        'не хватает ровно 383000 коп. до минимума'

    venue_settings "$(all_day_hours)" "$SEED_MIN_ORDER" "$SEED_DELIVERY_FEE" >/dev/null
}

scenario_U7() {
    cart_reset
    cart_add "$HV_005" 3
    assert_status 200 'позиция добавлена по старой цене'

    pos POST '/pos/scenario/raise-price/hv-005'
    assert_status 200 'заведение подняло цену'
    assert_json '.price_minor' '41400' 'новая цена заведения'

    if await_price hv-005 41400; then
        ok 'новая цена доехала до витрины'
    else
        bad 'новая цена не доехала до витрины за 20с'
    fi

    validate_cart
    assert_issue PRICE_CHANGED confirmable
    assert_true '[.issues[] | select(.code == "PRICE_CHANGED") | select(.old_price_minor == 36000 and .new_price_minor == 41400)] | length == 1' \
        'в проблеме старая и новая цена'

    place_order "$(idem_key u7-no)"
    assert_status 422 'оформление без подтверждения цены'
    assert_json '.code' 'CART_NOT_VALID' 'код ошибки'

    place_order "$(idem_key u7-yes)" ',"accept_price_changes":true'
    assert_status 201 'оформление с accept_price_changes'

    pos POST '/pos/scenario/reset-menu' >/dev/null
}

scenario_U8() {
    cart_reset
    cart_add "$HV_002" 1
    assert_status 200 'первая позиция из «Хачапури и вино»'

    cart_add "$TN_002" 1
    assert_status 409 'позиция из другого заведения'
    assert_json '.code' 'CART_MIXED_VENUES' 'код ошибки'
    assert_true '.detail | test("Хачапури и вино")' 'ошибка называет заведение корзины'
}

scenario_U9() {
    await_pos_idle || note 'на кухне остались заказы прошлого сценария'

    pos POST '/pos/scenario/reject-next'
    assert_status 200 'заведение отклонит следующий заказ'

    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    place_order "$(idem_key u9)"
    assert_status 201 'заказ оформлен'

    local order_id
    order_id=$(order_id_from_body)

    await_status "$order_id" 'rejected' 60 || return 1

    assert_true '.cancel_reason != null and .cancel_reason != ""' 'у отказа есть причина'
}

scenario_U10() {
    local budget
    budget=$(( $(duration_seconds "$ORDER_CONFIRMATION_TIMEOUT") + 60 ))

    await_pos_idle || note 'на кухне остались заказы прошлого сценария'

    pos POST '/pos/scenario/ignore-next'
    assert_status 200 'заведение не ответит на следующий заказ'

    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    place_order "$(idem_key u10)"
    assert_status 201 'заказ оформлен'

    local order_id
    order_id=$(order_id_from_body)

    note "ждём таймер подтверждения ($ORDER_CONFIRMATION_TIMEOUT), бюджет ${budget}с"
    await_status "$order_id" 'expired' "$budget" || return 1

    assert_true '.cancel_reason != null and .cancel_reason != ""' 'у автоотмены есть причина'
}

scenario_U11() {
    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    place_order "$(idem_key u11-cancel)"
    assert_status 201 'заказ оформлен'

    local order_id
    order_id=$(order_id_from_body)

    # Отмена возможна с placed: в created заказ ещё оформляется — холд
    # не подтверждён, заведение о нём не знает (docs/DESIGN.md §6.1).
    await_status "$order_id" 'placed accepted cooking' 60 || return 1

    pub POST "/orders/$order_id/cancel" '{"reason":"передумал"}'
    assert_status 200 'отмена до готовности'

    await_status "$order_id" 'cancelled' 60 || true

    # Вторая половина сценария: после «готово» отменять уже нельзя.
    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    place_order "$(idem_key u11-late)"
    assert_status 201 'второй заказ оформлен'

    order_id=$(order_id_from_body)

    await_status "$order_id" 'delivering completed' 120 || return 1

    pub POST "/orders/$order_id/cancel" '{"reason":"поздно"}'
    assert_status 409 'отмена после готовности'
    assert_json '.code' 'ORDER_NOT_CANCELLABLE' 'код ошибки'
}

scenario_U12() {
    pub GET '/search' '' -G --data-urlencode 'q=хачапур'
    assert_status 200 'поиск с опечаткой'
    assert_true '[.items[] | select(.item_name | test("Хачапури"))] | length >= 1' \
        'опечатка «хачапур» находит «Хачапури»'
}

scenario_U13() {
    cart_reset
    cart_add "$HV_001" 1 "$SIZE_L"
    assert_status 200 'позиция с модификатором «Большая»'

    pos POST '/pos/scenario/stop-option/hv-001-size-l'
    assert_status 200 'заведение сняло модификатор'

    sleep 2

    validate_cart
    assert_issue OPTION_UNAVAILABLE blocking
    assert_true '[.issues[] | select(.code == "OPTION_UNAVAILABLE") | .option_name] | length == 1' \
        'проблема называет модификатор'

    restore_availability >/dev/null
}

scenario_U14() {
    # Мок платежей отклоняет холд детерминированно — на сумме, оканчивающейся
    # на 99 копеек. Комиссия 149.99 ₽ и три хачапури по 450 ₽ дают ровно её.
    venue_settings "$(all_day_hours)" "$SEED_MIN_ORDER" 14999
    assert_status 200 'заведение выставило комиссию 14999 коп.'

    cart_reset
    cart_add "$HV_002" 3 >/dev/null

    place_order "$(idem_key u14)"
    assert_status 201 'заказ оформлен'
    assert_json '.total_amount_minor' '149999' 'сумма заказа оканчивается на 99 коп.'

    local order_id
    order_id=$(order_id_from_body)

    await_status "$order_id" 'payment_failed' 60 || true

    assert_true '[.timeline[].to_status] | index("placed") == null' \
        'заказ с неудачным холдом в заведение не уходил'

    venue_settings "$(all_day_hours)" "$SEED_MIN_ORDER" "$SEED_DELIVERY_FEE" >/dev/null
}

# --- сценарии заведения ----------------------------------------------------

scenario_M1() {
    pos POST '/pos/scenario/reset-menu'
    assert_status 200 'заведение импортировало меню и зарегистрировало вебхук'

    pub GET "/venues/$VENUE_1/menu"
    assert_status 200 'меню заведения в каталоге'
    assert_true '[.categories[].items[]] | length == 9' 'в каталоге девять позиций заведения'

    pub GET '/venues'
    assert_true "[.items[] | select(.id == \"$VENUE_1\")] | length == 1" 'заведение видно в каталоге'
}

scenario_M2() {
    pos POST '/pos/scenario/reset-menu'
    assert_status 200 'повторный импорт того же снапшота'
    assert_json '.idempotent' 'true' 'платформа распознала повтор'
    assert_json '.created' '0' 'создано позиций'
    assert_json '.updated' '0' 'обновлено позиций'
    assert_json '.archived' '0' 'заархивировано позиций'
}

scenario_M3() {
    mer POST '/menu/availability' '{"items":[{"external_id":"hv-006","is_available":false}]}'
    assert_status 200 'заведение сняло позицию через Merchant API'
    assert_json '.items_updated' '1' 'обновлено позиций'

    if await_availability hv-006 false; then
        ok 'позиция помечена недоступной в витрине'
    else
        bad 'витрина не увидела стоп-лист за 20с'
    fi

    pub GET '/search' '' -G --data-urlencode 'q=хинкали с сыром'
    assert_status 200 'поиск по снятой позиции'
    assert_true "[.items[] | select(.item_id == \"$HV_006\" and .is_available == false)] | length == 1" \
        'в выдаче позиция помечена недоступной'
    assert_true "(.items | map(.is_available) | index(false)) as \$first | \$first == null or ([.items[\$first:][] | select(.is_available)] | length == 0)" \
        'недоступные позиции опущены в конец выдачи'

    restore_availability >/dev/null
}

scenario_M4() {
    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    place_order "$(idem_key m4)"
    assert_status 201 'покупатель оформил заказ'

    local order_id
    order_id=$(order_id_from_body)

    await_status "$order_id" 'accepted cooking' 60 || return 1

    assert_true '.eta_minutes != null and .eta_minutes >= 1' 'заведение назвало ETA'

    mer GET "/orders/$order_id"
    assert_status 200 'заказ виден заведению в Merchant API'
}

scenario_M5() {
    await_pos_idle || note 'на кухне остались заказы прошлого сценария'

    pos POST '/pos/scenario/reject-next'
    assert_status 200 'повар отклонит следующий заказ'

    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    place_order "$(idem_key m5)"
    assert_status 201 'покупатель оформил заказ'

    local order_id
    order_id=$(order_id_from_body)

    await_status "$order_id" 'rejected' 60 || return 1

    mer GET "/orders/$order_id"
    assert_status 200 'заказ виден заведению'
    assert_json '.status' 'rejected' 'статус в Merchant API'
}

scenario_M6() {
    pos POST '/pos/scenario/go-offline?seconds=12'
    assert_status 200 'заведение ушло в офлайн — вебхук не пройдёт'

    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    place_order "$(idem_key m6)"
    assert_status 201 'заказ оформлен, пока заведение недоступно'

    local order_id
    order_id=$(order_id_from_body)

    note 'вебхук отбивается, заказ должен подобрать поллинг-фолбэк'
    await_status "$order_id" 'accepted cooking ready delivering completed' 90 || return 1

    ok 'заказ не потерялся: заведение забрало его поллингом'
}

scenario_M7() {
    mer PUT '/venue' '{"is_accepting_orders":false}'
    assert_status 200 'заведение поставило приём на паузу'

    pub GET "/venues/$VENUE_1"
    assert_json '.is_accepting_orders' 'false' 'каталог показывает паузу'
    assert_status 200 'заведение остаётся в каталоге'

    cart_reset
    cart_add "$HV_004" 3 >/dev/null

    place_order "$(idem_key m7)"
    assert_status 422 'оформление во время паузы'
    assert_true '[.issues[] | select(.code == "VENUE_NOT_ACCEPTING")] | length == 1' \
        'в ответе причина отказа'

    mer PUT '/venue' '{"is_accepting_orders":true}' >/dev/null
}

scenario_M8() {
    mer GET '/venue/stats'
    assert_status 200 'витрина статистики заведения'
    assert_true '.orders_total >= 1' 'заказы за день посчитаны'
    assert_true '.orders_completed >= 1' 'есть завершённые заказы'
    assert_true '.revenue_minor > 0' 'выручка посчитана'
    assert_true '.orders_rejected >= 1' 'отказы посчитаны'
}

# --- прогон ----------------------------------------------------------------

SCENARIOS='U1 U2 U3 U4 U5 U6 U7 U8 U9 U10 U11 U12 U13 U14 M1 M2 M3 M4 M5 M6 M7 M8'

title_for() {
    case $1 in
        U1)  printf 'Сквозной заказ от корзины до «доставлено»' ;;
        U2)  printf 'Товар кончился, пока лежал в корзине' ;;
        U3)  printf 'Заведение закрыто' ;;
        U4)  printf 'Заведение на паузе' ;;
        U5)  printf 'Адрес вне зоны доставки' ;;
        U6)  printf 'Не набран минимум заказа' ;;
        U7)  printf 'Цена выросла, пока корзина лежала' ;;
        U8)  printf 'Корзина из двух заведений' ;;
        U9)  printf 'Заведение отклонило заказ' ;;
        U10) printf 'Заведение не ответило — автоотмена по таймеру' ;;
        U11) printf 'Отмена пользователем: до готовности можно, после — нет' ;;
        U12) printf 'Опечатка в поиске' ;;
        U13) printf 'Недоступный модификатор' ;;
        U14) printf 'Холд оплаты не прошёл' ;;
        M1)  printf 'Онбординг заведения' ;;
        M2)  printf 'Повторный импорт меню идемпотентен' ;;
        M3)  printf 'Стоп-лист через Merchant API' ;;
        M4)  printf 'Приём заказа с ETA' ;;
        M5)  printf 'Отказ заведения' ;;
        M6)  printf 'POS был недоступен — заказ не потерялся' ;;
        M7)  printf 'Пауза приёма в час пик' ;;
        M8)  printf 'Витрина статистики' ;;
        *)   printf '%s' "$1" ;;
    esac
}

run_scenario() {
    local name=$1

    CURRENT_FAILED=0
    head1 "$name · $(title_for "$name")"

    "scenario_$name" || true

    if [ "$CURRENT_FAILED" -eq 0 ]; then
        SCENARIOS_PASSED=$((SCENARIOS_PASSED + 1))
    else
        SCENARIOS_FAILED=$((SCENARIOS_FAILED + 1))
        FAILED_LIST="$FAILED_LIST $name"
    fi
}

preflight() {
    command -v curl >/dev/null 2>&1 || die 'нужен curl'
    command -v jq >/dev/null 2>&1 ||
        die 'нужен jq — ассерты по JSON делаются им (macOS: brew install jq, Debian: apt install jq)'

    _req GET "$API_BASE/healthz" ''
    [ "$HTTP_STATUS" = 200 ] || die "kitchen-api не отвечает на $API_BASE (HTTP $HTTP_STATUS) — поднимите стенд: make up"

    _req GET "$POS_BASE/healthz" ''
    [ "$HTTP_STATUS" = 200 ] || die "merchant-pos не отвечает на $POS_BASE (HTTP $HTTP_STATUS)"

    pub GET "/venues/$VENUE_1"
    [ "$HTTP_STATUS" = 200 ] || die 'демо-данных нет в базе — накатите их: make seed'
}

main() {
    local requested="$*"
    local selected=$SCENARIOS
    local name known found

    if [ -n "$requested" ]; then
        selected=''

        for name in $requested; do
            found=0

            for known in $SCENARIOS; do
                [ "$name" = "$known" ] && found=1
            done

            [ "$found" = 1 ] || die "неизвестный сценарий: $name (есть: $SCENARIOS)"
            selected="$selected $name"
        done
    fi

    RUN_ID=$(date -u +%Y%m%d%H%M%S)
    # Отсечка прогона: заказы прошлых запусков остаются в SQLite заведения,
    # и «незакрытый заказ» из них не должен блокировать этот прогон.
    RUN_STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)

    printf '%s%s%s\n' "$C_BOLD" 'Авито.Кухня — прогон бизнес-сценариев' "$C_RESET"
    note "платформа $API_BASE · заведение $POS_BASE · прогон $RUN_ID"
    note "таймер подтверждения заказа: $ORDER_CONFIRMATION_TIMEOUT (ORDER_CONFIRMATION_TIMEOUT=25s make up ускоряет прогон)"

    preflight
    trap teardown EXIT
    setup

    for name in $selected; do
        run_scenario "$name"
    done

    # Уборку зовём явно, чтобы итог прогона оставался последним, что видно
    # в терминале. Ловушка остаётся на случай аварийного выхода.
    trap - EXIT
    teardown

    head1 'Итог'

    printf '  сценарии: %s%d пройдено%s' "$C_GREEN" "$SCENARIOS_PASSED" "$C_RESET"

    if [ "$SCENARIOS_FAILED" -gt 0 ]; then
        printf ', %s%d провалено:%s%s\n' "$C_RED" "$SCENARIOS_FAILED" "$FAILED_LIST" "$C_RESET"
    else
        printf '\n'
    fi

    printf '  проверки: %s%d пройдено%s' "$C_GREEN" "$CHECKS_PASSED" "$C_RESET"

    if [ "$CHECKS_FAILED" -gt 0 ]; then
        printf ', %s%d провалено%s\n' "$C_RED" "$CHECKS_FAILED" "$C_RESET"
    else
        printf '\n'
    fi

    if [ "$SCENARIOS_FAILED" -gt 0 ] || [ "$CHECKS_FAILED" -gt 0 ]; then
        printf '\n%s%s%s\n' "$C_RED$C_BOLD" 'ПРОГОН НЕ ПРОЙДЕН' "$C_RESET"

        return 1
    fi

    printf '\n%s%s%s\n' "$C_GREEN$C_BOLD" 'ВСЕ СЦЕНАРИИ ПРОЙДЕНЫ' "$C_RESET"
}

main "$@"
