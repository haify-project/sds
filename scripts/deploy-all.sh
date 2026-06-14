#!/bin/bash
# Build and deploy SDS to the given hosts.
# Usage: ./scripts/deploy-all.sh orange1,orange2,orange3
#
# Cross-compiles for the nodes (linux/amd64 by default; override with
# TARGET_OS/TARGET_ARCH) and is self-HA aware (see deploy.sh).

set -e

HOSTS=${1:-"orange1"}

echo ">>> Building (cross-compiled) and deploying to: $HOSTS"
./scripts/deploy.sh --hosts "$HOSTS" --build

echo ">>> Deployment complete."
