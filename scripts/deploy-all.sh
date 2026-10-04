#!/bin/bash
# Build and deploy SDS to the given hosts.
# Usage: ./scripts/deploy-all.sh node1,node2,node3
#
# Cross-compiles for each node's own architecture (TARGET_ARCH forces one),
# checks every node gets a binary built for it, and is self-HA aware (see
# deploy.sh).

set -e

HOSTS=${1:?usage: $0 host1,host2,...}

echo ">>> Building (cross-compiled) and deploying to: $HOSTS"
./scripts/deploy.sh --hosts "$HOSTS" --build

echo ">>> Deployment complete."
