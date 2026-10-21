#!/bin/sh
# End-to-end smoke test of the Docker Compose setup and the ./pgo wrapper:
# starts PostgreSQL, migrates, starts the web server and checks it. Uses a
# throwaway .env and data directory and cleans up after.
#
# Usage: scripts/compose-smoke-test.sh   (from anywhere; needs Docker)
#
# With SMOKE_DIR set to an extracted release bundle (scripts/release-bundle.sh),
# it tests the bundle instead: its docker-compose.yml and ./pgo, with the image
# in PAPERLESS_GO_IMAGE, which must exist locally (the bundle builds nothing).
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "${SMOKE_DIR:-$repo}"

if [ -e .env ]; then
	echo "refusing to run: .env exists and would be overwritten" >&2
	exit 1
fi

work=$(mktemp -d)

# Own Compose project, image tag and random host ports, so the test does
# not touch a local paperless-go installation.
COMPOSE_PROJECT_NAME=paperless-go-smoke
PAPERLESS_GO_IMAGE=${PAPERLESS_GO_IMAGE:-paperless-go:smoke}
export COMPOSE_PROJECT_NAME PAPERLESS_GO_IMAGE

# Data files belong to the container user (UID 65532), so on Linux the host
# user may not be able to delete them. Inspect and remove them from a
# container instead, reusing the PostgreSQL image (it has a shell).
in_data() {
	docker compose --progress quiet run --rm --no-deps -T --entrypoint sh \
		-v "$work/data:/data" postgres -c "$1"
}

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then
		echo "--- smoke test failed, logs follow ---" >&2
		docker compose logs postgres >&2 || true
		docker compose logs web >&2 || true
	fi
	in_data 'rm -rf /data/* /data/.[!.]*' >/dev/null 2>&1 || true
	docker compose --progress quiet down -v >/dev/null 2>&1 || true
	rm -f .env
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

# No data directory: the first ./pgo migrate creates it, as on a real first start.
cat > .env <<ENV
POSTGRES_PASSWORD=smoke-test
DATA_DIR=$work/data
POSTGRES_PORT=${SMOKE_POSTGRES_PORT:-0}
WEB_PORT=${SMOKE_WEB_PORT:-0}
ENV

# Remove what an interrupted previous run left behind.
docker compose down -v --remove-orphans >/dev/null 2>&1 || true

expect() { # expect <file> <extended regex>
	if ! grep -Eq "$2" "$1"; then
		echo "expected output matching: $2" >&2
		cat "$1" >&2
		exit 1
	fi
}

echo "== migrate"
./pgo migrate | tee "$work/out"
expect "$work/out" "applied 00001_initial.sql"
if [ "$(uname -s)" = Linux ]; then
	owner=$(in_data 'stat -c %u:%g /data' | tr -d '\r')
	if [ "$owner" != 65532:65532 ]; then
		echo "expected the data directory to belong to 65532:65532, got $owner" >&2
		exit 1
	fi
fi

echo "== migrate again"
./pgo migrate | tee "$work/out"
expect "$work/out" "database schema is up to date"

echo "== migrate status"
./pgo migrate status | tee "$work/out"
expect "$work/out" "00001_initial.sql +applied"

echo "== version"
./pgo --version | tee "$work/out"
expect "$work/out" "^paperless-go version "

echo "== web, as in README.txt"
docker compose --progress quiet up -d --wait web
WEB_PORT=$(docker compose port web 8080 | sed 's/.*://')
base="http://localhost:$WEB_PORT"
get() { curl -fsS --retry 10 --retry-delay 1 --retry-all-errors "$base$1"; }
get /healthz > "$work/out"
expect "$work/out" '^ok$'
get / > "$work/out"
expect "$work/out" '<div id="root">'
asset=$(grep -o '/assets/[^"]*\.js' "$work/out" | head -1)
get "$asset" > /dev/null
status=$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.example' "$base/healthz")
if [ "$status" != 403 ]; then
	echo "expected 403 for a foreign Host header, got $status" >&2
	exit 1
fi
status=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
	-H 'Origin: http://evil.example' -d '{}' "$base/api/anything")
if [ "$status" != 403 ]; then
	echo "expected 403 for a cross-origin POST, got $status" >&2
	exit 1
fi

echo "== health without database"
docker compose --progress quiet stop postgres
status=$(curl -s -o /dev/null -w '%{http_code}' "$base/healthz")
if [ "$status" != 503 ]; then
	echo "expected 503 from /healthz without the database, got $status" >&2
	exit 1
fi
docker compose --progress quiet start postgres

echo "== backup"
./pgo backup "$work/backups" | tee "$work/out"
expect "$work/out" "wrote .*paperless-[0-9TZ]+\.dump"
dump=$(ls "$work/backups"/paperless-*.dump)
docker compose --progress quiet exec -T postgres pg_restore --list < "$dump" > "$work/out"
expect "$work/out" 'TABLE public installation'

echo "smoke test passed"
