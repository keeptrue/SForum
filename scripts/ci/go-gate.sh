#!/usr/bin/env bash
# Go-side CI gate: isolated database tests plus Go command build.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

echo "Running Go tests plus the V3 P12 compatibility farm on one disposable cluster..."
./scripts/test-api-isolated.sh --no-cache --with-compat

echo "Building Go commands..."
(cd apps/api && go build ./...)
