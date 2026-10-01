#!/usr/bin/env bash
# Fails when a hand-written source file reaches 600 lines. Generated code
# (protobuf, swagger) and build output are exempt.
set -euo pipefail
limit=${1:-600}
cd "$(dirname "$0")/.."
over=$(git ls-files '*.go' '*.ts' '*.tsx' '*.pm' '*.sh' '*.py' \
	| grep -vE '(\.pb\.go|\.pb\.gw\.go|_grpc\.pb\.go)$|^(third_party|ui|website)/|/node_modules/' \
	| xargs wc -l | awk -v l="$limit" '$2 != "total" && $1 >= l { print $1, $2 }' | sort -rn)
if [ -n "$over" ]; then
	echo "Files at or over $limit lines (split them by responsibility):"
	echo "$over"
	exit 1
fi
