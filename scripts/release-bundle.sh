#!/bin/sh
# Builds the Docker Compose bundle of a release: docker-compose.yml with the
# image of this version instead of a local build, .env.example, the ./pgo
# wrapper and a README, in paperless-go/ inside OUTDIR/paperless-go.tar.gz,
# plus OUTDIR/SHA256SUMS.
#
# Usage: scripts/release-bundle.sh VERSION OUTDIR [IMAGE]
#   VERSION  YY.MM-N, for example 26.10-1
#   IMAGE    default ghcr.io/pklnx/paperless-go
set -eu

version=${1:?usage: release-bundle.sh VERSION OUTDIR [IMAGE]}
out=${2:?usage: release-bundle.sh VERSION OUTDIR [IMAGE]}
image=${3:-ghcr.io/pklnx/paperless-go}
if ! echo "$version" | grep -Eq '^[0-9]{2}\.[0-1][0-9]-[1-9][0-9]*$'; then
	echo "not a release version (YY.MM-N): $version" >&2
	exit 2
fi

repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
dir=$stage/paperless-go
mkdir "$dir"

# The services pull the released image; the build sections go. A build
# section is the "build:" line of a service and the lines indented deeper.
{
	echo "# paperless-go $version: https://github.com/pklnx/paperless-go/releases/tag/v$version"
	echo "# Upgrade: extract a newer release's paperless-go.tar.gz in the directory"
	echo "# above this one (it keeps .env and data/), then ./pgo migrate and"
	echo "# docker compose up -d web."
	echo "#"
	awk '
		/^    build:/ { skip = 1; next }
		skip && /^      / { next }
		{ skip = 0 }
		{ print }
	' "$repo/docker-compose.yml" |
		sed -e "s|\${PAPERLESS_GO_IMAGE:-paperless-go:local}|\${PAPERLESS_GO_IMAGE:-$image:$version}|" \
			-e 's| (image built by ./pgo)||'
} > "$dir/docker-compose.yml"
if grep -q 'build:' "$dir/docker-compose.yml" || ! grep -q "$image:$version" "$dir/docker-compose.yml"; then
	echo "could not rewrite docker-compose.yml" >&2
	exit 1
fi

cp "$repo/.env.example" "$dir/.env.example"
cp "$repo/pgo" "$dir/pgo"
chmod 0755 "$dir/pgo"
cat > "$dir/README.txt" <<EOF
paperless-go $version

Image:   $image:$version (linux/amd64, linux/arm64)
Docs:    https://pklnx.github.io/paperless-go/
Release: https://github.com/pklnx/paperless-go/releases/tag/v$version

First start:
  cp .env.example .env     # set POSTGRES_PASSWORD
  ./pgo keygen             # set PAPERLESS_GO_SECRET_KEY in .env to its output
  ./pgo migrate            # also makes data/ writable for the container
  docker compose up -d web # http://localhost:8080
  ./pgo user add NAME --admin   # the first admin; log in and set up 2FA

Upgrade: back up first, then extract the newer paperless-go.tar.gz in the
directory above this one, so that it replaces these files and keeps .env
and data/:
  ./pgo migrate
  docker compose up -d web
EOF

# Numeric owner 0/0, not the build machine's user.
tar -C "$stage" --owner=0 --group=0 --numeric-owner -czf "$out/paperless-go.tar.gz" paperless-go
(cd "$out" && sha256sum paperless-go.tar.gz > SHA256SUMS)
echo "$out/paperless-go.tar.gz"
