#!/bin/sh
# Prints the head of a release's notes, in Markdown: the image line, new
# installation and upgrade. The release workflow passes it to `gh release
# create --notes-file`, which appends the notes generated from the pull
# requests (.github/release.yml). For the first semver release (LAST empty)
# it adds a paragraph for automatic image updaters, since the old `26.10`
# tag stays behind.
#
# Usage: scripts/release-notes.sh VERSION IMAGE LAST
#   VERSION  X.Y.Z, for example 1.0.0
#   IMAGE    for example ghcr.io/pklnx/paperless-go
#   LAST     the last semver version; empty for the first semver release
set -eu

if [ $# -ne 3 ]; then
	echo "usage: release-notes.sh VERSION IMAGE LAST" >&2
	exit 2
fi
version=$1 image=$2 last=$3
minor=${version%.*} major=${version%%.*}

cat << EOF
Image: \`$image:$version\` (linux/amd64, linux/arm64), also tagged \`$minor\`, \`$major\` and \`latest\`.

**New installation:** download \`paperless-go.tar.gz\` below, extract it and follow \`README.txt\` or the [getting started guide](https://pklnx.github.io/paperless-go/guide/getting-started).

**Upgrade:** back up, extract \`paperless-go.tar.gz\` in the directory above your \`paperless-go/\` directory (it keeps \`.env\` and \`data/\`), then \`./pgo migrate\` and \`docker compose up -d web\`. Read the database migrations and breaking changes below first; see [Upgrades](https://pklnx.github.io/paperless-go/guide/operations#upgrades).
EOF

[ -z "$last" ] || exit 0

cat << EOF

**First release with semantic versioning.** The earlier release was named \`YY.MM-N\` (\`26.10-1\`); its tag and images stay. The image tag \`26.10\` stays on \`26.10-1\` and gets no more updates. If a tool updates the image automatically (Watchtower, Renovate, Diun), switch it once to an exact version or \`$minor\` (\`$major\` also brings minor releases, whose migrations stop the web server until \`./pgo migrate\` runs): on \`26.10\` it gets nothing new, tools that compare tags may rank \`26.10\` above \`$version\`, and on \`latest\` it installs this release without its upgrade steps. See [Versions](https://pklnx.github.io/paperless-go/guide/operations#versions).
EOF
