#!/bin/sh
# Tests scripts/ci-pull-images.sh --list. Offline: fixture Dockerfiles and
# docker-compose.yml files in a temp folder (CI_PULL_IMAGES_ROOT), plus the
# real repository.
set -u

here=$(cd "$(dirname "$0")" && pwd)
script=$here/ci-pull-images.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
n=0

nl='
'

# run_case <name> <want rc> <want stdout lines, as printed after "images:"> <Dockerfile> <compose>
# An empty compose argument writes no image. The listed images are compared as
# a whole, so an extra or missing image fails.
run_case() {
	name=$1 want_rc=$2 want_out=$3 dockerfile=$4 compose=$5
	n=$((n + 1))
	root=$tmp/case$n
	mkdir -p "$root"
	printf '%s' "$dockerfile" >"$root/Dockerfile"
	printf 'services:\n  app:\n%s' "$compose" >"$root/docker-compose.yml"
	rc=0
	CI_PULL_IMAGES_ROOT=$root sh "$script" --list >"$tmp/out" 2>"$tmp/err" || rc=$?
	got=$(sed -n '2,$p' "$tmp/out" | sed 's/^  //')
	if [ "$rc" != "$want_rc" ]; then
		echo "FAIL $name: exit $rc, want $want_rc" >&2
		fail=1
	elif [ "$rc" = 0 ] && [ "$got" != "$want_out" ]; then
		echo "FAIL $name: listed '$got', want '$want_out'" >&2
		fail=1
	else
		echo "ok   $name"
	fi
}

# fail_msg <name> <text expected in stderr of the last run_case>
fail_msg() {
	if ! grep -q -- "$2" "$tmp/err"; then
		echo "FAIL $1: stderr lacks '$2'" >&2
		fail=1
	fi
}

compose_pg='    image: postgres:18
'

run_case plain 0 "alpine:3" "FROM alpine:3
" ""
run_case scratch 0 "postgres:18" "FROM scratch
" "$compose_pg"
run_case scratch-platform-as 0 "postgres:18" "FROM --platform=\$BUILDPLATFORM scratch AS final
" "$compose_pg"
run_case scratch-upper 0 "postgres:18" "FROM SCRATCH
" "$compose_pg"
run_case scratch-stage-name 0 "postgres:18" "FROM scratch AS final
FROM final AS out
" "$compose_pg"
run_case scratch-among-others 0 "alpine:3${nl}postgres:18" "FROM alpine:3 AS build
FROM scratch
COPY --from=build /x /x
" "$compose_pg"
run_case stage-reference 0 "alpine:3" "FROM alpine:3 AS build
FROM build AS x
" ""
run_case syntax-frontend 0 "docker/dockerfile:1${nl}alpine:3" "# syntax=docker/dockerfile:1
FROM alpine:3
" ""
# shellcheck disable=SC2016 # literal ${...} in the fixtures
run_case compose-variable 0 "alpine:3" "FROM alpine:3
" '    image: ${PAPERLESS_GO_IMAGE:-paperless-go:local}
'
# shellcheck disable=SC2016 # literal ${...} in the fixtures
run_case from-variable 1 "" 'ARG IMAGE=alpine:3
FROM ${IMAGE}
' ""
fail_msg from-variable "cannot read FROM image"
run_case from-continuation 1 "" "FROM alpine:3 \\
  AS build
" ""
fail_msg from-continuation "continues on the next line"
run_case scratch-only-no-compose 1 "" "FROM scratch
" ""
fail_msg scratch-only-no-compose "no images found"

# The real repository: the variable must not redirect it, and the list must
# have images and no scratch.
rc=0
env -u CI_PULL_IMAGES_ROOT sh "$script" --list >"$tmp/out" 2>"$tmp/err" || rc=$?
if [ "$rc" != 0 ]; then
	echo "FAIL real-repo: exit $rc" >&2
	fail=1
elif [ "$(sed -n '2,$p' "$tmp/out" | grep -c .)" -lt 1 ]; then
	echo "FAIL real-repo: no images listed" >&2
	fail=1
elif grep -qix '[[:space:]]*scratch' "$tmp/out"; then
	echo "FAIL real-repo: scratch listed" >&2
	fail=1
else
	echo "ok   real-repo"
fi

exit "$fail"
