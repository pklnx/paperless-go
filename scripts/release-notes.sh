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

**First release with semantic versioning.** The earlier release was named \`YY.MM-N\` (\`26.10-1\`); its tag and images stay. The image tag \`26.10\` stays on \`26.10-1\` and gets no more updates. If a tool updates the image automatically (Watchtower, Renovate, Diun), switch it once to an exact version or \`$minor\` (\`$major\` also brings minor releases, which may include a migration; until \`./pgo migrate\` runs, the web server does not start). \`$minor\` gets nothing new once a newer minor or major release is out: then move the tag to the newest \`X.Y\` tag (for example \`$major.1\`, or \`$((major + 1)).0\` after the major release's upgrade steps; set \`PAPERLESS_GO_IMAGE\`, run \`./pgo migrate\`, then \`docker compose up -d web\`) after reading that release's notes. On \`26.10\` a tool gets nothing new, and tools that compare tags may rank \`26.10\` above \`$version\`; a tool on \`latest\` installs a release without its upgrade steps, and after a migration the web server does not start until \`./pgo migrate\` runs. See [Versions](https://pklnx.github.io/paperless-go/guide/operations#versions).
EOF
