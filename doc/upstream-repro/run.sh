#!/usr/bin/env bash
# Воспроизведение ошибок go_client апстрима и проверка правок.
#
#   ./run.sh                 — клонирует апстрим во временный каталог
#   ./run.sh <клон>          — берёт готовый клон (его рабочее дерево сбрасывается)
#
# Шаги: коммит fae121e без правок — тесты падают; с патчами 1, 2, 3, 5 —
# проходят. Отдельно — гонка 5б со вставленной задержкой, до и после правок.
# Нужны git и Go 1.26; сеть — только для клонирования и go mod tidy.
# Весь прогон — около двух с половиной минут: 5а без правки ждёт 45 с, 5б — 65 с.
set -euo pipefail

COMMIT=fae121efc3ef57b633516601d3c0d6b1be1fde7c
REPO=https://github.com/SpaceNeuroX/proxy-turn-vk-android
HERE=$(cd "$(dirname "$0")" && pwd)
# В go.mod апстрима — «go 1.26», а версии с таким именем для скачивания нет:
# называем точную.
export GOTOOLCHAIN=${GOTOOLCHAIN:-go1.26.2}

if [ $# -ge 1 ]; then
	CLONE=$(cd "$1" && pwd)
else
	CLONE=$(mktemp -d)/proxy-turn-vk-android
	git clone -q "$REPO" "$CLONE"
fi
GC=$CLONE/go_client

reset() {
	git -C "$CLONE" checkout -q "$COMMIT"
	git -C "$CLONE" checkout -q -- .
	rm -f "$GC/repro_test.go"
}
trap reset EXIT

# Задержка 300 мс перед SetReadDeadline в чтении сеанса — окно гонки 5б
# из микросекунд становится заметным. Только для демонстрации.
inject_delay() {
	sed -i 's/^\(\t*\)\(_ = activeConn\.SetReadDeadline(time\.Now()\.Add(sessionReadTimeout))\)$/\1time.Sleep(300 * time.Millisecond) \/\/ REPRO: окно гонки\n\1\2/' "$GC/session.go"
	[ "$(grep -c 'REPRO: окно гонки' "$GC/session.go")" = 1 ] || { echo "задержка не вставилась" >&2; exit 1; }
}

run_tests() {
	# Код возврата go test не важен: падения здесь и есть результат.
	(cd "$GC" && "$@" go test -tags upstream_repro -count=1 -timeout 10m -v -run "$TESTS" . 2>&1 || true) |
		grep -E '^\s*--- (PASS|FAIL|SKIP)|repro_test\.go:[0-9]+:' || true
}

reset
cp "$HERE/repro_test.go" "$GC/"
(cd "$GC" && go mod tidy >/dev/null)

echo "== $COMMIT без правок"
TESTS='TestRepro' run_tests env
echo
echo "== без правок, гонка 5б с задержкой"
inject_delay
TESTS='TestReproStopRightAfterSessionStart' run_tests env REPRO_INJECT_DELAY=1
git -C "$CLONE" checkout -q -- go_client/session.go

for p in 1-closed-call 2-baton 3-config-quota 5-stop; do
	git -C "$CLONE" apply "$HERE/$p.patch"
done
echo
echo "== с правками 1, 2, 3, 5"
TESTS='TestRepro' run_tests env
echo
echo "== с правками, гонка 5б с задержкой"
inject_delay
TESTS='TestReproStopRightAfterSessionStart' run_tests env REPRO_INJECT_DELAY=1
