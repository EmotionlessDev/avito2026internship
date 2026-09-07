#!/usr/bin/env bash
#
# check-coverage.sh — порог покрытия доменных пакетов.
#
# Порог живёт в скрипте, а не в глазах проверяющего: цифра, за которой никто
# не следит автоматически, сползает вниз молча.
#
# Считается объединённое покрытие юнит- и интеграционных тестов: репозитории
# закрываются только на настоящем Postgres, и мерить их отдельно бессмысленно.
# Профиль собирается с -coverpkg, поэтому каждый тестовый бинарь пишет блоки
# всех пакетов; при подсчёте блоки сводятся по максимуму — блок, покрытый хотя
# бы одним бинарём, считается покрытым.

set -euo pipefail

THRESHOLD=${COVERAGE_THRESHOLD:-70}
PROFILE=${COVERAGE_PROFILE:-coverage.out}
ROOT=$(cd "$(dirname "$0")/.." && pwd)

cd "$ROOT"

# Домен — то, ради чего написан проект. Инфраструктурная обвязка (pg, logger,
# kafkax, httpx, config) и merchant-pos — отдельная система — под порог
# не попадают: гнать их до той же цифры значило бы писать тесты ради цифры.
DOMAIN_PACKAGES="
internal/cart
internal/catalog
internal/merchant
internal/order
internal/order/workflow
internal/paging
internal/payments
internal/search
internal/stats
"

if [ "${COVERAGE_SKIP_TESTS:-}" != "1" ]; then
    echo "прогоняю тесты с покрытием (включая интеграционные — нужен docker)"
    go test ./... -coverpkg=./internal/... -coverprofile="$PROFILE" >/dev/null
fi

[ -s "$PROFILE" ] || {
    echo "профиль покрытия пуст: $PROFILE" >&2
    exit 1
}

python3 - "$PROFILE" "$THRESHOLD" "$DOMAIN_PACKAGES" <<'PY'
import collections
import os
import sys

profile, threshold, domain = sys.argv[1], float(sys.argv[2]), sys.argv[3].split()
module = 'github.com/emotionlessdev/avito-kitchen/'

# Один и тот же блок приезжает из каждого тестового бинаря: берём максимум,
# иначе покрытие делилось бы на число бинарей.
blocks = {}
for line in open(profile, encoding='utf-8'):
    line = line.strip()
    if not line or line.startswith('mode:'):
        continue

    where, statements, count = line.rsplit(' ', 2)
    statements, count = int(statements), int(count)
    known = blocks.get(where)
    if known is None or count > known[1]:
        blocks[where] = (statements, count)

total = collections.Counter()
covered = collections.Counter()
for where, (statements, count) in blocks.items():
    package = os.path.dirname(where.split(':')[0]).replace(module, '')
    total[package] += statements
    if count > 0:
        covered[package] += statements

failed = []
print(f'\nпокрытие доменных пакетов (порог {threshold:.0f}%):\n')

for package in domain:
    if not total[package]:
        print(f'  {package:<26} нет кода')
        continue

    percent = 100 * covered[package] / total[package]
    mark = 'ok  ' if percent >= threshold else 'НИЖЕ'
    print(f'  {mark} {package:<26} {percent:5.1f}%  ({covered[package]}/{total[package]})')

    if percent < threshold:
        failed.append((package, percent))

rest = sorted(set(total) - set(domain))
if rest:
    print('\nостальные пакеты (под порог не попадают):\n')
    for package in rest:
        percent = 100 * covered[package] / total[package]
        print(f'       {package:<26} {percent:5.1f}%')

if failed:
    print('\nниже порога:')
    for package, percent in failed:
        print(f'  {package} — {percent:.1f}%')
    sys.exit(1)

print('\nвсе доменные пакеты выше порога')
PY
