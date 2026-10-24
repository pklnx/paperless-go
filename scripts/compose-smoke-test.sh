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
		-v "$work/data:/data" -v "$work/consume:/consume" postgres -c "$1"
}

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then
		echo "--- smoke test failed, logs follow ---" >&2
		docker compose logs postgres >&2 || true
		docker compose logs web >&2 || true
	fi
	if [ -n "${oneoff:-}" ]; then
		docker rm -f "$oneoff" >/dev/null 2>&1 || true
	fi
	# Stop the app first: a running web server's worker can still write under
	# /data/work as the container's user while it is removed, and rm below fails.
	docker compose --progress quiet stop >/dev/null 2>&1 || true
	in_data 'rm -rf /data/* /data/.[!.]* /consume/* /consume/.[!.]*' >/dev/null 2>&1 || true
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
CONSUME_DIR=$work/consume
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
# The container sees the data directory: migrate binds it to the
# installation, so the sweep's daily walk may run there.
expect "$work/out" "^bound the data directory /data to this installation$"
if [ "$(uname -s)" = Linux ]; then
	owner=$(in_data 'stat -c %u:%g /data' | tr -d '\r')
	if [ "$owner" != 65532:65532 ]; then
		echo "expected the data directory to belong to 65532:65532, got $owner" >&2
		exit 1
	fi
	owner=$(in_data 'stat -c %u:%g /consume' | tr -d '\r')
	if [ "$owner" != 65532:65532 ]; then
		echo "expected the consume folder to belong to 65532:65532, got $owner" >&2
		exit 1
	fi
fi

# A new database gets PostgreSQL's builtin locale (docker-compose.yml).
echo "== database locale"
docker compose --progress quiet exec -T postgres psql -U paperless -d paperless -Atc \
	"SELECT datlocprovider, datlocale FROM pg_database WHERE datname = 'paperless'" | tee "$work/out"
expect "$work/out" '^b\|C\.UTF-8$'

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
expect "$work/out" 'imported: 1, duplicates: 0, restored: 0, failed: 0'
./pgo import "$work/scans" | tee "$work/out"
expect "$work/out" 'sub/smoke.pdf: duplicate'
if ./pgo import "$work/scans/sub/smoke.pdf" > "$work/out" 2>&1; then
	echo "a file instead of a folder must be refused" >&2
	exit 1
fi
expect "$work/out" 'is not a directory'

# A scan generated in the image (a PDF page rendered to pixels, without a
# text layer) is imported and OCRed by `process --until-empty`, which
# returns when the queue is empty: no waiting. serve is stopped meanwhile,
# so it does not take the job; it gives a running one back.
echo "== OCR of a generated scan"
docker compose --progress quiet stop web
mkdir "$work/ocr"
chmod 777 "$work/ocr" # the container user writes the scan
docker compose --progress quiet run --rm --no-deps -T --entrypoint sh -v "$work/ocr:/ocr" paperless-go -c '
	set -e
	python3 -I -c "
import sys
text = b\"BT /F1 24 Tf 72 720 Td (Rechnung 4711 vom 1. Oktober 2026) Tj ET\"
objs = [b\"<< /Type /Catalog /Pages 2 0 R >>\", b\"<< /Type /Pages /Kids [3 0 R] /Count 1 >>\",
        b\"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\",
        b\"<< /Length %d >>\nstream\n%s\nendstream\" % (len(text), text),
        b\"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\"]
out, offsets = bytearray(b\"%PDF-1.4\n\"), []
for i, o in enumerate(objs, 1):
    offsets.append(len(out))
    out += b\"%d 0 obj\n%s\nendobj\n\" % (i, o)
xref = len(out)
out += b\"xref\n0 %d\n0000000000 65535 f \n\" % (len(objs) + 1)
for o in offsets:
    out += b\"%010d 00000 n \n\" % o
out += b\"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n\" % (len(objs) + 1, xref)
open(sys.argv[1], \"wb\").write(out)
" /tmp/page.pdf
	pdftoppm -r 200 -gray -png -singlefile /tmp/page.pdf /tmp/page
	img2pdf --imgsize 200dpi -o /ocr/scan.pdf /tmp/page.png
	chmod 644 /ocr/scan.pdf
	echo "text layer: $(pdftotext /ocr/scan.pdf - | wc -w) words"
' | tee "$work/out"
expect "$work/out" '^text layer: 0 words$'
./pgo import "$work/ocr" | tee "$work/out"
expect "$work/out" 'scan.pdf: imported'
./pgo process --until-empty | tee "$work/out"
expect "$work/out" '^processed: 1, to retry: 0,'

# One pass and one walk, with serve stopped and the queue empty: the data
# directory is bound (by migrate), so the walk runs.
echo "== sweep"
./pgo sweep | tee "$work/out"
expect "$work/out" '^queue: removed: 0, in use again: 0, failed: 0$'
expect "$work/out" '^walk: files: [1-9][0-9]*, unreferenced: [0-9]+, queued: [0-9]+, '

echo "== processing status, thumbnail and retry in the API"
docker compose --progress quiet up -d --wait web
WEB_PORT=$(docker compose port web 8080 | sed 's/.*://')
base="http://localhost:$WEB_PORT"
# json <file> <expression>: evaluates a Python expression on the JSON in
# file (as d) and prints the result.
json() {
	python3 -I -c 'import json, sys; d = json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$1" "$2"
}
curl -fsS --retry 10 --retry-delay 1 --retry-all-errors -b "$work/cookies" "$base/api/documents" > "$work/list"
id=$(json "$work/list" '[x["id"] for x in d["documents"] if x["originalFileName"] == "scan.pdf"][0]')
thumb=$(json "$work/list" '[x["thumbnailUrl"] for x in d["documents"] if x["id"] == "'"$id"'"][0]')
json "$work/list" '[(x["originalFileName"], x["processing"]["state"]) for x in d["documents"]]' | tee "$work/out"
expect "$work/out" "\('scan.pdf', 'done'\)"
expect "$work/out" "\('smoke.pdf', 'failed'\)"
curl -fsS -b "$work/cookies" "$base/api/documents/$id" > "$work/doc"
json "$work/doc" 'd["textSource"] + ": " + d["extractedText"]' | tee "$work/out"
expect "$work/out" '^Tesseract: .*Rechnung'
expect "$work/out" '4711'
case $thumb in
/api/documents/"$id"/thumbnail\?v=*) ;;
*)
	echo "unexpected thumbnailUrl: $thumb" >&2
	exit 1
	;;
esac
curl -fsS -b "$work/cookies" -D "$work/headers" -o "$work/thumb.jpg" "$base$thumb"
expect "$work/headers" '^[Cc]ontent-[Tt]ype: image/jpeg'
expect "$work/headers" '^[Cc]ache-[Cc]ontrol: private, max-age=86400, immutable'
if [ "$(od -An -tx1 -N3 "$work/thumb.jpg" | tr -d ' \n')" != ffd8ff ]; then
	echo "the thumbnail is no JPEG" >&2
	exit 1
fi
etag=$(sed -n 's/^[Ee][Tt]ag: *//p' "$work/headers" | tr -d '\r')
status=$(curl -s -o /dev/null -w '%{http_code}' -b "$work/cookies" -H "If-None-Match: $etag" "$base$thumb")
if [ "$status" != 304 ]; then
	echo "expected 304 for a cached thumbnail, got $status" >&2
	exit 1
fi
status=$(curl -s -o "$work/out" -w '%{http_code}' -b "$work/cookies" -H 'Content-Type: application/json' \
	-H "Origin: $base" -d '{}' "$base/api/documents/$id/reprocess")
if [ "$status" != 202 ]; then
	echo "expected 202 from reprocess, got $status" >&2
	cat "$work/out" >&2
	exit 1
fi
json "$work/out" 'd["processing"]["state"]' > "$work/state"
expect "$work/state" '^(queued|running|done)$'

# The counts and the backup come from one window with the web server
# stopped, so no background write (audit log, jobs) falls in between.
echo "== backup"
docker compose --progress quiet stop web
sql() { # sql <database> <query>
	docker compose --progress quiet exec -T postgres psql -X -qAt -v ON_ERROR_STOP=1 -U paperless -d "$1" -c "$2"
}
audit_before=$(sql paperless 'SELECT count(*) FROM audit_log')
docs_before=$(sql paperless 'SELECT count(*) FROM documents')
./pgo backup "$work/backups" | tee "$work/out"
expect "$work/out" "wrote .*paperless-[0-9TZ]+\.dump"
dump=$(ls "$work/backups"/paperless-*.dump)
docker compose --progress quiet exec -T postgres pg_restore --list < "$dump" > "$work/out"
expect "$work/out" 'TABLE public installation'

# ./pgo restore replaces the database with the dump. The database has audit
# log partitions (every one from migration 00005 on), on which the
# pg_restore --clean the guide used to prescribe fails (#121).
echo "== restore: refusals"
# A change after the backup, which only a restore undoes.
echo "marker password" | ./pgo user add marker --password-stdin | tee "$work/out"
expect "$work/out" 'user "marker" created'
marker() { sql paperless "SELECT count(*) FROM users WHERE name = 'marker'"; }
# refuses <status> <regex> [arguments]: ./pgo restore with the arguments
# exits with status and a message matching regex, and changes nothing.
# It runs with $restore_path as PATH, so a test can put stand-ins first.
restore_path=$PATH
refuses() {
	want=$1
	msg=$2
	shift 2
	got=0
	PATH=$restore_path ./pgo restore "$@" > "$work/out" 2>&1 || got=$?
	cat "$work/out"
	if [ "$got" != "$want" ]; then
		echo "expected exit status $want from ./pgo restore $*, got $got" >&2
		exit 1
	fi
	expect "$work/out" "$msg"
	if [ "$(marker)" != 1 ]; then
		echo "a refused restore changed the database" >&2
		exit 1
	fi
	sql postgres 'SELECT datname FROM pg_database' > "$work/dbs"
	if grep -Eq '^paperless_(restore|replaced_)' "$work/dbs"; then
		echo "a refused restore left a database behind" >&2
		cat "$work/dbs" >&2
		exit 1
	fi
	if [ "$(sql postgres "SELECT datallowconn FROM pg_database WHERE datname = 'paperless'")" != t ]; then
		echo "a refused restore left the database closed to connections" >&2
		exit 1
	fi
}
refuses 2 'usage: \./pgo restore'
refuses 2 'cannot read' --yes "$work/missing.dump"
refuses 2 'restore --yes FILE' "$dump"
docker compose --progress quiet up -d --wait web
refuses 1 'still running: web$' --yes "$dump"
docker compose --progress quiet stop web
# A running ./pgo command (a shell in the CLI container stands in for an
# import) blocks it too.
oneoff=$(docker compose --progress quiet run -d --rm --no-deps --entrypoint sh paperless-go -c 'exec sleep infinity')
refuses 1 'still running: paperless-go$' --yes "$dump"
docker rm -f "$oneoff" > /dev/null
oneoff=
# When Docker Compose cannot list the containers, that counts as busy: a
# stand-in for docker fails `compose ps` and runs everything else.
mkdir "$work/failps"
cat > "$work/failps/docker" <<EOF
#!/bin/sh
case " \$* " in *" ps "*) echo "compose ps failed" >&2; exit 1 ;; esac
exec $(command -v docker) "\$@"
EOF
chmod +x "$work/failps/docker"
restore_path=$work/failps:$PATH
refuses 1 'could not list the containers' --yes "$dump"
restore_path=$PATH

echo "== restore: the name for the replaced database is taken"
# A stand-in for date fixes the name ./pgo restore picks.
mkdir "$work/fixeddate"
printf '#!/bin/sh\necho 20000101_000000\n' > "$work/fixeddate/date"
chmod +x "$work/fixeddate/date"
sql postgres 'CREATE DATABASE paperless_replaced_20000101_000000'
got=0
PATH=$work/fixeddate:$PATH ./pgo restore --yes "$dump" > "$work/out" 2>&1 || got=$?
cat "$work/out"
if [ "$got" != 1 ]; then
	echo "expected exit status 1 for a taken name, got $got" >&2
	exit 1
fi
expect "$work/out" 'the database paperless_replaced_20000101_000000 exists already; nothing was changed'
sql postgres 'DROP DATABASE paperless_replaced_20000101_000000'
refuses 2 'restore --yes FILE' "$dump" # the checks of refuses: nothing changed

echo "== restore: a broken dump"
# The last bytes cut off: they belong to the table data after the table of
# contents, so the archive passes the check before the restore and
# pg_restore itself fails.
size=$(wc -c < "$dump")
head -c $((size - 32)) "$dump" > "$work/broken.dump"
docker compose --progress quiet exec -T postgres pg_restore --list < "$work/broken.dump" > /dev/null
refuses 1 'pg_restore failed' --yes "$work/broken.dump"

echo "== restore: the swap fails"
# Another session holds a lock on the database (an open COMMENT ON DATABASE,
# from the maintenance database, so the restore does not end it): the rename
# fails at its lock timeout and the restored copy is dropped again.
mkfifo "$work/lock.in" "$work/lock.out"
docker compose --progress quiet exec -T postgres psql -X -Atq -U paperless -d postgres \
	< "$work/lock.in" > "$work/lock.out" &
exec 3> "$work/lock.in" 4< "$work/lock.out"
echo "BEGIN; COMMENT ON DATABASE paperless IS 'smoke'; SELECT 'locked';" >&3
read -r locked <&4
if [ "$locked" != locked ]; then
	echo "could not lock the database: $locked" >&2
	exit 1
fi
refuses 1 'could not swap in the restored database' --yes "$dump"
echo "ROLLBACK;" >&3
exec 3>&- 4<&-
wait

echo "== restore"
# A leftover of an aborted restore is dropped first, and a database left
# closed to connections by an interrupted swap is opened again.
sql postgres 'CREATE DATABASE paperless_restore'
sql postgres 'ALTER DATABASE paperless ALLOW_CONNECTIONS false'
./pgo restore --yes "$dump" | tee "$work/restore.out"
expect "$work/restore.out" 'did not accept connections .*; opening it again'
expect "$work/restore.out" '^restored '
expect "$work/restore.out" 'kept as paperless_replaced_[0-9]{8}_[0-9]{6};'
if [ "$(marker)" != 0 ]; then
	echo "the restore kept the user added after the backup" >&2
	exit 1
fi
rows_are() { # rows_are <table> <count>
	got=$(sql paperless "SELECT count(*) FROM $1")
	if [ "$got" != "$2" ]; then
		echo "expected $2 rows in $1 after the restore, got $got" >&2
		exit 1
	fi
}
rows_are audit_log "$audit_before"
rows_are documents "$docs_before"
sql postgres 'SELECT datname FROM pg_database' > "$work/dbs"
if grep -q '^paperless_restore$' "$work/dbs" || [ "$(grep -c '^paperless_replaced_' "$work/dbs")" != 1 ]; then
	echo "expected one replaced database and no paperless_restore" >&2
	cat "$work/dbs" >&2
	exit 1
fi
# The replaced database is kept as it was and opens again (to undo).
replaced=$(grep '^paperless_replaced_' "$work/dbs")
if [ "$(sql "$replaced" "SELECT count(*) FROM users WHERE name = 'marker'")" != 1 ]; then
	echo "the replaced database lost the user added after the backup" >&2
	exit 1
fi
./pgo migrate status | tee "$work/out"
if grep -q pending "$work/out"; then
	echo "expected no pending migration after the restore" >&2
	exit 1
fi
expect "$work/out" '00001_initial\.sql +applied'
# The data directory still belongs to the restored database (the same
# installation): the sweep's walk accepts it.
./pgo sweep --dry-run | tee "$work/out"
expect "$work/out" '^walk: files: [1-9][0-9]*, unreferenced: [0-9]+, would queue: [0-9]+, '
docker compose --progress quiet up -d --wait web
WEB_PORT=$(docker compose port web 8080 | sed 's/.*://')
base="http://localhost:$WEB_PORT"
# The login from before the backup is in the dump.
curl -fsS --retry 10 --retry-delay 1 --retry-all-errors -b "$work/cookies" "$base/api/users" > "$work/out"
expect "$work/out" '"name":"smoke"'
if grep -q '"name":"marker"' "$work/out"; then
	echo "the API still lists the user added after the backup" >&2
	exit 1
fi

echo "== restore: drop the replaced database"
# The command ./pgo restore printed, as it is.
drop=$(sed -n 's/^  \(docker compose exec postgres dropdb .*\)$/\1/p' "$work/restore.out")
echo "$drop"
sh -c "$drop" < /dev/null
if [ "$(sql postgres "SELECT count(*) FROM pg_database WHERE datname = '$replaced'")" != 0 ]; then
	echo "the printed dropdb command did not drop $replaced" >&2
	exit 1
fi

echo "smoke test passed"
