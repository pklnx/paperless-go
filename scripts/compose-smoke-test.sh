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

# No data directory: the first ./pgo command (keygen) creates it as Docker
# does, and ./pgo migrate makes it writable, as on a real first start.
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

echo "== keygen"
./pgo keygen | tee "$work/out"
key=$(tr -d '\r\n' < "$work/out")
expect "$work/out" '^[A-Za-z0-9+/]{43}=$'
echo "PAPERLESS_GO_SECRET_KEY=$key" >> .env

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

# The OCR tools in the image, as the container user.
echo "== OCR tools"
docker compose --progress quiet run --rm --no-deps -T --entrypoint sh paperless-go -c '
	set -e
	echo "uid $(id -u)"
	tesseract --list-langs
	echo "ocrmypdf $(ocrmypdf --version)"
	for tool in pdfinfo pdftotext pdftoppm img2pdf heif-dec; do
		command -v "$tool"
	done
	heif-dec --list-decoders
' | tee "$work/out"
expect "$work/out" '^uid 65532$'
expect "$work/out" '^deu$'
expect "$work/out" '^eng$'
expect "$work/out" '^osd$' # --rotate-pages needs it
expect "$work/out" '^ocrmypdf [0-9]+\.'
expect "$work/out" '/heif-dec$'
expect "$work/out" '^- libde265 '

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

echo "== login"
status=$(curl -s -o /dev/null -w '%{http_code}' "$base/api/users")
if [ "$status" != 401 ]; then
	echo "expected 401 from the API without a login, got $status" >&2
	exit 1
fi
echo "correct horse battery" | ./pgo user add smoke --admin --password-stdin | tee "$work/out"
expect "$work/out" 'admin "smoke" created'
curl -fsS -c "$work/cookies" -H 'Content-Type: application/json' -H "Origin: $base" \
	-d '{"username":"smoke","password":"correct horse battery"}' "$base/api/session" > "$work/out"
expect "$work/out" '"name":"smoke"'
# An admin without 2FA can only set it up.
expect "$work/out" '"twoFactorRequired":true'
status=$(curl -s -o "$work/out" -w '%{http_code}' -b "$work/cookies" "$base/api/users")
if [ "$status" != 403 ]; then
	echo "expected 403 before the 2FA setup, got $status" >&2
	exit 1
fi
expect "$work/out" '"twoFactorSetupRequired":true'
post() { # post <path> <json>: with the session cookie
	curl -fsS -b "$work/cookies" -c "$work/cookies" -H 'Content-Type: application/json' -H "Origin: $base" -d "$2" "$base$1"
}
post /api/profile/2fa/setup '{}' > "$work/out"
expect "$work/out" '"qrDataUrl":"data:image/png;base64,'
secret=$(sed -n 's/.*"secret":"\([A-Z2-7]*\)".*/\1/p' "$work/out")
# The current TOTP code (RFC 6238, SHA-1, 6 digits, 30 s).
totp() {
	python3 -I -c 'import base64,hmac,struct,sys,time
k=base64.b32decode(sys.argv[1]+"="*(-len(sys.argv[1])%8))
h=hmac.digest(k,struct.pack(">Q",int(time.time())//30),"sha1")
o=h[-1]&15
print("%06d"%((struct.unpack(">I",h[o:o+4])[0]&0x7fffffff)%1000000))' "$secret"
}
post /api/profile/2fa/confirm "{\"code\":\"$(totp)\"}" > "$work/out"
expect "$work/out" '"recoveryCodes":\["[a-z0-9-]+"'
recovery=$(sed -n 's/.*"recoveryCodes":\["\([a-z0-9-]*\)".*/\1/p' "$work/out")
curl -fsS -b "$work/cookies" "$base/api/users" > "$work/out"
expect "$work/out" '"twoFactorEnabled":true'
expect "$work/out" '"self":true'
# The next login needs the second factor; a recovery code works.
curl -fsS -c "$work/cookies" -H 'Content-Type: application/json' -H "Origin: $base" \
	-d '{"username":"smoke","password":"correct horse battery"}' "$base/api/session" > "$work/out"
challenge=$(sed -n 's/.*"challenge":"\([^"]*\)".*/\1/p' "$work/out")
post /api/session/2fa "{\"challenge\":\"$challenge\",\"code\":\"$recovery\"}" > "$work/out"
expect "$work/out" '"name":"smoke"'
curl -fsS -b "$work/cookies" "$base/api/users" > "$work/out"
expect "$work/out" '"self":true'

echo "== health without database"
docker compose --progress quiet stop postgres
status=$(curl -s -o /dev/null -w '%{http_code}' "$base/healthz")
if [ "$status" != 503 ]; then
	echo "expected 503 from /healthz without the database, got $status" >&2
	exit 1
fi
docker compose --progress quiet start postgres

echo "== import"
# The container user (65532) must be able to read the mounted folder.
mkdir -p "$work/scans/sub"
printf '%%PDF-1.4\n%% smoke\n%%%%EOF\n' > "$work/scans/sub/smoke.pdf"
chmod -R a+rX "$work/scans"
./pgo import "$work/scans" --dry-run | tee "$work/out"
expect "$work/out" 'sub/smoke.pdf: would import'
./pgo import "$work/scans" | tee "$work/out"
expect "$work/out" 'sub/smoke.pdf: imported'
expect "$work/out" 'imported: 1, duplicates: 0, failed: 0'
./pgo import "$work/scans" | tee "$work/out"
expect "$work/out" 'sub/smoke.pdf: duplicate'
if ./pgo import "$work/scans/sub/smoke.pdf" > "$work/out" 2>&1; then
	echo "a file instead of a folder must be refused" >&2
	exit 1
fi
expect "$work/out" 'is not a directory'

echo "== backup"
./pgo backup "$work/backups" | tee "$work/out"
expect "$work/out" "wrote .*paperless-[0-9TZ]+\.dump"
dump=$(ls "$work/backups"/paperless-*.dump)
docker compose --progress quiet exec -T postgres pg_restore --list < "$dump" > "$work/out"
expect "$work/out" 'TABLE public installation'

echo "smoke test passed"
