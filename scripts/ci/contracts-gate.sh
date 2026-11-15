#!/usr/bin/env bash
# Contracts/static CI gate: proto drift, OpenAPI refs, architecture and
# documentation validation. No database required.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

echo "Running Protobuf lint and generated SDK drift validation..."
./scripts/proto.sh check

echo "Running Host API v2 SDK documentation drift validation..."
node tests/validate-host-api-v2-docs.mjs

echo "Running OpenAPI reference validation..."
ruby scripts/validate-openapi-refs.rb

echo "Running contract status-code parity validation..."
node tests/validate-contract-status-parity.mjs

echo "Running staged extension management contract validation..."
node tests/validate-staged-extension-contracts.js

echo "Running V3 production trust deployment validation..."
node tests/validate-v3-production-trust.mjs

echo "Running production WebSocket proxy validation..."
node tests/validate-production-websocket-proxy.mjs

echo "Running architecture boundary validation..."
node tests/validate-architecture-boundaries.mjs

echo "Running documentation and version-governance validation..."
node tests/validate-docs.mjs

echo "Running identity UI validation..."
node tests/validate-identity-ui.js

echo "Running homepage validation..."
node tests/validate-homepage.js

echo "Running public SEO validation..."
bun tests/validate-public-seo.ts

echo "Running SEO workbench validation..."
bun tests/validate-seo-workbench.ts

echo "Running moderation workbench validation..."
bun tests/validate-moderation-workbenches.ts

echo "Running theme runtime validation..."
node tests/validate-theme-runtime.js

echo "Running trusted admin runtime validation..."
node tests/validate-trusted-admin-runtime.js

echo "Running synchronous theme activation validation..."
node tests/validate-theme-activation.js

echo "Running development worker script validation..."
node tests/validate-dev-worker-script.js

echo "Running Signal Garden theme validation..."
node tests/validate-signal-garden-theme.js

echo "Running SF component library validation..."
node tests/validate-sf-components.js

echo "Running Runtime Page Registry offline contracts (+ optional live HTTP smoke)..."
node tests/validate-page-registry-runtime.js

echo "Running V3 platform catalog and traceability validation..."
node tests/validate-v3-p0-catalogs.mjs

echo "Running public page runtime surface catalog validation..."
node tests/validate-public-page-surface-catalog.mjs

if [ -d apps/web/node_modules ]; then
  echo "Running web unit tests for trusted editor L2 and registry catalogs..."
  (cd apps/web && bun test \
    tests/framework/editorL2Load.test.ts \
    tests/framework/editorDomLoadOrder.test.ts \
    tests/admin/adminRegistryCatalogs.test.ts \
    tests/identity/authProvidersPublicUi.test.ts \
    tests/identity/authRouteRendering.test.ts \
    tests/admin/adminLoginMethods.test.ts \
    tests/admin/adminMailNotifications.test.ts \
    tests/identity/accountSecurityM4b.test.ts)
  echo "Running admin framework validation..."
  bun tests/validate-admin-framework.ts
else
  if [ "${CI:-}" = "true" ]; then
    echo "apps/web/node_modules is required in CI; run bun install --frozen-lockfile first." >&2
    exit 1
  fi
  echo "Skipping web contract unit tests because apps/web/node_modules is missing."
fi
