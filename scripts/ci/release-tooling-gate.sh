#!/usr/bin/env bash
# Release/deploy tooling gate: validates the release pipeline itself rather
# than product code. Runs in release-tooling.yml and as part of local test.sh.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

echo "Running release script validation..."
./scripts/release_test.sh

echo "Running exact main CI release gate validation..."
./scripts/ci/verify-main-ci_test.sh

echo "Running release workflow architecture validation..."
ruby ./scripts/ci/release_workflow_test.rb

echo "Running automatic release notes validation..."
./scripts/ci/generate-release-notes_test.sh

echo "Running release asset packaging validation..."
./scripts/ci/release_assets_test.sh

echo "Running anonymous release image pull validation..."
./scripts/ci/verify-public-images_test.sh

echo "Running PostgreSQL backup and restore safety validation..."
./deploy/scripts/postgres-safety_test.sh

echo "Running release deployment state-machine validation..."
./deploy/scripts/deploy_test.sh

echo "Running production Compose migration configuration validation..."
./deploy/scripts/compose-production_test.sh

echo "Running zero-downtime Compose topology validation..."
./deploy/scripts/zero-downtime-compose_test.sh

echo "Running zero-downtime release selection validation..."
./deploy/scripts/upgrade-version-selection_test.sh

echo "Running verified release bootstrap validation..."
./deploy/scripts/bootstrap_test.sh

echo "Running one-line install loader validation..."
./deploy/scripts/install-loader_test.sh

echo "Running zero-downtime state-machine validation..."
./deploy/scripts/zero-downtime-state_test.sh

echo "Running blue/green slot health probe validation..."
./deploy/scripts/upgrade-health-probe_test.sh

echo "Running documentation validation failure-path tests..."
./tests/validate-docs_test.sh

echo "Running publishable Web SDK package validation..."
node ./scripts/ci/publish-web-sdks_test.mjs
node ./scripts/ci/verify-web-sdks.mjs

echo "Running built-in plugin release version validation..."
node tests/validate-builtin-plugin-versions.mjs

echo "Running verified latest-wrapper validation..."
./tests/validate-sforum-latest.sh
