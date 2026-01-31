#!/bin/bash
# Deploy SDS to specified hosts
# Usage: ./scripts/deploy-all.sh orange1,orange2,orange3

set -e

# 第一个参数作为 hosts，默认为 orange1
HOSTS=${1:-"orange1"}

echo ">>> Building binaries..."
make build

echo ">>> Deploying to: $HOSTS"
./scripts/deploy.sh --hosts "$HOSTS"

echo ">>> Deployment Complete."
