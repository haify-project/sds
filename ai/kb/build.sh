#!/usr/bin/env bash
# Builds the SDS shared knowledge base: the knowledge that is the same on every
# cluster SDS runs on. One file, versioned with the code, installed on every
# node and attached to sds-ai read-only (SDS_AI_SHARED_KNOWLEDGE_DB). What a
# cluster learns about itself — incident notes, what was tried on which machine
# — goes into that cluster's own knowledge base, never into this one.
#
# Contents:
#   - SDS documentation (docs/, README)
#   - the SDS code graph (.understand-anything/knowledge-graph.json) and source
#   - the sds-cli reference, generated from the binary this commit builds, so
#     it can never describe flags the installed CLI does not have
#   - the service-ip OCF agent's code graph, when its checkout is present
#   - DRBD 9 documentation: the LINBIT knowledge base, the DRBD 9 user's guide
#     (English) and the AI-assistant notes
#
# Every cluster must query it with the embedder it was built with; the model
# and dimension are recorded in the manifest next to the database.
#
# Usage: ai/kb/build.sh [out-dir]          (default: dist/kb)
#
# Environment:
#   STEWARD_EMB_BASE_URL / _API_KEY / _MODEL, SDS_KB_EMB_DIM   embedder
#   STEWARD_LLM_BASE_URL / _API_KEY / _MODEL                   entity extraction
#   SDS_KB_CORPUS     dir holding linbit-blog-kb/, linbit-documentation/, ai-assistants/
#   SERVICE_IP_REPO   service-ip checkout (optional)
#   STEWARD           steward binary (default: installed at the pinned version)
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
out=${1:-$root/dist/kb}
corpus=${SDS_KB_CORPUS:?set SDS_KB_CORPUS to the dir with linbit-blog-kb/, linbit-documentation/, ai-assistants/}
: "${STEWARD_EMB_API_KEY:?}" "${STEWARD_EMB_MODEL:?}" "${STEWARD_EMB_BASE_URL:?}"
: "${STEWARD_LLM_API_KEY:?}" "${STEWARD_LLM_MODEL:?}" "${STEWARD_LLM_BASE_URL:?}"
export STEWARD_EMB_DIM=${SDS_KB_EMB_DIM:-768}
steward_version=v0.51.0
od=${STEWARD:-}
if [ -z "$od" ]; then
	od=$(mktemp -d)/steward
	GOBIN=$(dirname "$od") GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org \
		go install "github.com/liliang-cn/steward/cmd/steward@$steward_version"
fi

version=$(git -C "$root" describe --tags --always --dirty)
db=$out/sds-kb.db
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p "$out"
rm -f "$db" "$db"-wal "$db"-shm
export STEWARD_KNOWLEDGE_DB_PATH=$db
export STEWARD_DOMAIN_FILE=$root/ai/domain.toml

# flatten <src-dir> <prefix> <find-args...>: copies matching files into one
# staging dir as *.md, naming each after its path so document ids stay unique
# and say where the text came from.
flatten() {
	local src=$1 prefix=$2 dir
	shift 2
	dir=$stage/$prefix
	mkdir -p "$dir"
	(cd "$src" && find . -type f "$@") | while read -r f; do
		f=${f#./}
		cp "$src/$f" "$dir/${prefix}__$(echo "${f%.*}" | tr '/' '_').md"
	done
	echo "$dir"
}

step() { printf '\n==> %s\n' "$*"; }

step "SDS documentation"
docs=$(flatten "$root/docs" sds-docs -name '*.md')
cp "$root/README.md" "$docs/sds-docs__README.md"
"$od" ingest "$docs"

step "SDS code graph"
"$od" import-graph "$root/.understand-anything/knowledge-graph.json"

step "SDS source"
"$od" ingest-repo "$root"

step "sds-cli reference (built from this commit)"
cli=$stage/sds-cli
(cd "$root" && go build -o "$cli" ./cmd/cli)
"$od" ingest-cli "$cli"

if [ -n "${SERVICE_IP_REPO:-}" ] && [ -f "$SERVICE_IP_REPO/.understand-anything/knowledge-graph.json" ]; then
	step "service-ip code graph"
	"$od" import-graph "$SERVICE_IP_REPO/.understand-anything/knowledge-graph.json"
fi

step "DRBD 9 documentation"
"$od" ingest "$(flatten "$corpus/linbit-blog-kb" linbit-kb -name '*.md')"
# DRBD 9 only: the 8.4 guide describes behaviour SDS does not have.
"$od" ingest "$(flatten "$corpus/linbit-documentation/UG9/en" drbd9-guide -name '*.adoc')"
"$od" ingest "$(flatten "$corpus/ai-assistants" drbd-ai-notes -name '*.md')"

step "manifest"
sqlite3 "$db" 'PRAGMA wal_checkpoint(TRUNCATE); VACUUM;'
sha=$(shasum -a 256 "$db" | cut -d' ' -f1)
cat >"$out/sds-kb.json" <<EOF
{
  "version": "$version",
  "built": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "sha256": "$sha",
  "embedder": {"model": "$STEWARD_EMB_MODEL", "dim": $STEWARD_EMB_DIM},
  "extractor": {"model": "$STEWARD_LLM_MODEL"},
  "steward": "$steward_version"
}
EOF
cat "$out/sds-kb.json"
