#!/usr/bin/env bash
# Builds the SDS shared knowledge base: the knowledge that is the same on every
# cluster SDS runs on. One file, versioned with the code, installed on every
# node and attached to sds-ai read-only (SDS_AI_SHARED_KNOWLEDGE_DB). What a
# cluster learns about itself — incident notes, what was tried on which machine
# — goes into that cluster's own knowledge base, never into this one.
#
# Contents:
#   - SDS documentation (docs/, README) and the operations runbooks
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
# Every document and step is retried, and recorded in <out-dir>/progress when
# it succeeds: a build takes hours, and the embedder behind it — a node that
# may be busy or asleep — answers 503 now and then. SDS_KB_RESUME=1 keeps the
# database and the record and carries on where the last run stopped; without
# it the build starts from nothing.
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
progress=$out/progress
if [ "${SDS_KB_RESUME:-0}" != 1 ]; then
	rm -f "$db" "$db"-wal "$db"-shm "$progress"
fi
touch "$progress"
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

# once <key> <cmd...>: runs cmd unless key is recorded as done, retrying a
# failure with backoff, and records key when it succeeds.
once() {
	local key=$1 try
	shift
	grep -qxF "$key" "$progress" && return 0
	for try in 1 2 3 4 5; do
		if "$@"; then
			echo "$key" >>"$progress"
			return 0
		fi
		echo "   $key failed (attempt $try); retrying in $((try * 20))s" >&2
		sleep $((try * 20))
	done
	echo "giving up on $key; rerun with SDS_KB_RESUME=1" >&2
	return 1
}

# ingest_one <file>: ingests one document on its own, so a failure costs that
# document rather than the directory it came from.
ingest_one() {
	local f=$1 one
	one=$stage/one/$(basename "$f" .md)
	mkdir -p "$one"
	cp "$f" "$one/"
	once "doc:$(basename "$f")" "$od" ingest "$one"
	rm -rf "$one"
}

# ingest_each <dir>: ingests a flattened dir, SDS_KB_JOBS documents at a time
# (default 4). Almost all of a document's time is spent waiting on the
# extraction model, so documents in parallel is what makes the build take
# minutes per hundred documents rather than hours; SQLite serialises the
# brief writes between them. Waits on the oldest job when all slots are busy,
# which bash 3.2 (the macOS /bin/bash) can do and `wait -n` cannot.
ingest_each() {
	local f jobs=${SDS_KB_JOBS:-4} failed=0
	local pids=()
	for f in "$1"/*; do
		ingest_one "$f" &
		pids+=($!)
		if [ ${#pids[@]} -ge "$jobs" ]; then
			wait "${pids[0]}" || failed=1
			pids=(${pids[@]+"${pids[@]:1}"})
		fi
	done
	for f in ${pids[@]+"${pids[@]}"}; do
		wait "$f" || failed=1
	done
	return $failed
}

step "SDS documentation"
docs=$(flatten "$root/docs" sds-docs -name '*.md')
cp "$root/README.md" "$docs/sds-docs__README.md"
ingest_each "$docs"

# The operations runbooks the MCP server serves to agents: the same text, so
# the Copilot answers a procedure question with the procedure it would be given.
step "SDS runbooks"
ingest_each "$(flatten "$root/pkg/mcpserver/runbooks" sds-runbooks -name '*.md')"

step "SDS code graph"
once step:sds-code-graph "$od" import-graph "$root/.understand-anything/knowledge-graph.json"

step "SDS source"
once step:sds-source "$od" ingest-repo "$root"

step "sds-cli reference (built from this commit)"
cli=$stage/sds-cli
(cd "$root" && go build -o "$cli" ./cmd/cli)
once step:sds-cli "$od" ingest-cli "$cli"

if [ -n "${SERVICE_IP_REPO:-}" ] && [ -f "$SERVICE_IP_REPO/.understand-anything/knowledge-graph.json" ]; then
	step "service-ip code graph"
	once step:service-ip-graph "$od" import-graph "$SERVICE_IP_REPO/.understand-anything/knowledge-graph.json"
fi

step "DRBD 9 documentation"
ingest_each "$(flatten "$corpus/linbit-blog-kb" linbit-kb -name '*.md')"
# DRBD 9 only: the 8.4 guide describes behaviour SDS does not have.
ingest_each "$(flatten "$corpus/linbit-documentation/UG9/en" drbd9-guide -name '*.adoc')"
ingest_each "$(flatten "$corpus/ai-assistants" drbd-ai-notes -name '*.md')"

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
