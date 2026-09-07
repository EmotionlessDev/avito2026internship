#!/usr/bin/env bash
#
# check-diagrams.sh — прогоняет все Mermaid-блоки репозитория через mermaid-cli.
#
# Нужен потому, что сломанная диаграмма на GitHub выглядит как пустой блок,
# а не как ошибка: глазами такое не ловится, а читатель видит дыру вместо
# схемы. Самые обидные ловушки — символы, которые Mermaid считает синтаксисом:
# точка с запятой внутри текста разбивает строку на два выражения.
#
# Рендерер запускается в контейнере: тянуть Node и Chromium на машину ради
# проверки markdown незачем.

set -euo pipefail

IMAGE=${MERMAID_IMAGE:-minlag/mermaid-cli:latest}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)

cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

command -v docker >/dev/null 2>&1 || {
    echo "нужен docker: рендерер mermaid запускается в контейнере" >&2
    exit 1
}

python3 - "$ROOT" "$WORK" <<'PY'
import glob, os, re, sys

root, work = sys.argv[1], sys.argv[2]
sources = ['README.md', 'PLAN.md', 'docs/DESIGN.md'] + sorted(
    os.path.relpath(p, root) for p in glob.glob(f'{root}/docs/adr/*.md'))

total = 0
for relative in sources:
    path = os.path.join(root, relative)
    if not os.path.exists(path):
        continue

    with open(path, encoding='utf-8') as handle:
        text = handle.read()

    for number, match in enumerate(re.finditer(r'```mermaid\n(.*?)```', text, re.S), start=1):
        total += 1
        line = text[:match.start()].count('\n') + 1
        name = f'{total:02d}__{relative.replace("/", "_")[:-3]}__line{line}.mmd'
        with open(os.path.join(work, name), 'w', encoding='utf-8') as out:
            out.write(match.group(1))
PY

count=$(find "$WORK" -name '*.mmd' | wc -l | tr -d ' ')

if [ "$count" = 0 ]; then
    echo "диаграмм не найдено — проверять нечего" >&2
    exit 1
fi

echo "проверяю $count диаграмм"

failed=0

for file in "$WORK"/*.mmd; do
    name=$(basename "$file" .mmd)

    if output=$(docker run --rm -v "$WORK:/data" "$IMAGE" \
            -i "/data/$(basename "$file")" -o "/data/$name.svg" 2>&1) &&
        [ -s "$WORK/$name.svg" ]; then
        printf '  ok   %s\n' "$name"
    else
        printf '  ОШИБКА %s\n' "$name"
        printf '%s\n' "$output" | grep -A3 'Parse error' || printf '%s\n' "$output" | tail -5
        failed=$((failed + 1))
    fi
done

if [ "$failed" -gt 0 ]; then
    echo "не отрендерилось диаграмм: $failed" >&2
    exit 1
fi

echo "все диаграммы рендерятся"
