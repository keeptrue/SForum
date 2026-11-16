#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export LANG=C

VERSION_INPUT="${1:-}"
VERSION="${VERSION_INPUT#v}"
ASSET_DIR="${2:-}"

if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || [[ ! -d "$ASSET_DIR" ]]; then
  echo "Usage: finalize-release-assets.sh VERSION ASSET_DIR" >&2
  exit 2
fi

EXPECTED=(
  "sforum-cli_${VERSION}_darwin_amd64.tar.gz"
  "sforum-cli_${VERSION}_darwin_arm64.tar.gz"
  "sforum-cli_${VERSION}_linux_amd64.tar.gz"
  "sforum-cli_${VERSION}_linux_arm64.tar.gz"
  "sforum-server_${VERSION}_linux_amd64.tar.gz"
  "sforum-server_${VERSION}_linux_arm64.tar.gz"
  "sforum-deploy.tar.gz"
  "sforum-bootstrap.sh"
  "upgrade.sh"
  "sforum-install.sh"
  # RV-15：latest 包装器与它的校验和是 Release 资产，不再从 main 分支取。
  "sforum-latest.sh"
  "sforum-latest.sh.sha256"
)

for asset in "${EXPECTED[@]}"; do
  if [[ ! -s "$ASSET_DIR/$asset" ]]; then
    echo "Missing release asset: $asset" >&2
    exit 1
  fi
done

ACTUAL="$(find "$ASSET_DIR" -maxdepth 1 -type f ! -name SHA256SUMS -exec basename {} \; | sort)"
SORTED_EXPECTED="$(printf '%s\n' "${EXPECTED[@]}" | sort)"
if [[ "$ACTUAL" != "$SORTED_EXPECTED" ]]; then
  echo "Release asset set does not match the expected matrix" >&2
  printf 'Actual:\n%s\n' "$ACTUAL" >&2
  exit 1
fi

for os in darwin linux; do
  for arch in amd64 arm64; do
    cli_root="sforum-cli_${VERSION}_${os}_${arch}"
    cli_listing="$(tar -tzf "$ASSET_DIR/$cli_root.tar.gz")"
    grep -qx "$cli_root/sforum" <<< "$cli_listing" || {
      echo "CLI archive is missing $cli_root/sforum" >&2
      exit 1
    }
  done
done

for arch in amd64 arm64; do
  server_root="sforum-server_${VERSION}_linux_${arch}"
  server_listing="$(tar -tzf "$ASSET_DIR/$server_root.tar.gz")"
  for binary in sforum-api sforum-migrate sforum; do
    grep -qx "$server_root/bin/$binary" <<< "$server_listing" || {
      echo "Server archive is missing $server_root/bin/$binary" >&2
      exit 1
    }
  done
  if grep -qx "$server_root/bin/sforum-worker" <<< "$server_listing"; then
    echo "Server archive must not expose a standalone Worker binary" >&2
    exit 1
  fi
  grep -q "^$server_root/extensions/builtin/" <<< "$server_listing" || {
    echo "Server archive is missing protected built-ins" >&2
    exit 1
  }
done

deploy_listing="$(tar -tzf "$ASSET_DIR/sforum-deploy.tar.gz")"
# backup-postgres.sh / restore-postgres.sh 的第一条 source 行就要 postgres-common.sh
# 与 postgres-backup-format.sh；漏一个是「装完才炸」的缺口，所以这里逐个点名断言，
# 不靠目录通配，也不依赖 bootstrap 提升清单的顺序。
for entry in \
  "sforum-deploy/sforum-bootstrap.sh" \
  "sforum-deploy/deploy.sh" \
  "sforum-deploy/upgrade.sh" \
  "sforum-deploy/compose.yaml" \
  "sforum-deploy/compose.prod.yaml" \
  "sforum-deploy/compose.release.yaml" \
  "sforum-deploy/compose.zero-downtime.yaml" \
  "sforum-deploy/.env.production.example" \
  "sforum-deploy/VERSION" \
  "sforum-deploy/deploy/scripts/configure-production.sh" \
  "sforum-deploy/deploy/scripts/backup-postgres.sh" \
  "sforum-deploy/deploy/scripts/postgres-common.sh" \
  "sforum-deploy/deploy/scripts/postgres-backup-format.sh" \
  "sforum-deploy/deploy/scripts/restore-postgres.sh" \
  "sforum-deploy/deploy/scripts/wait-for-health.sh" \
  "sforum-deploy/deploy/scripts/deploy-lock.sh" \
  "sforum-deploy/deploy/caddy/Caddyfile" \
  "sforum-deploy/deploy/router/Caddyfile.example" \
  "sforum-deploy/deploy/runtime/.gitignore" \
  "sforum-deploy/deploy/runtime/.gitkeep"; do
  grep -qx "$entry" <<< "$deploy_listing" || {
    echo "Deploy archive is missing $entry" >&2
    exit 1
  }
done
# GitHub Actions artifacts do not preserve Unix file modes: the
# upload-artifact -> download-artifact cross-job transfer lands executable
# files with 0644. The finalizer must normalize real artifact input on its
# own (release.yml must not need a chmod workaround), so content checks run
# first, then the execute bit is restored before the -x post-check and
# checksum generation.
for script in sforum-bootstrap.sh upgrade.sh sforum-install.sh sforum-latest.sh; do
  [[ "$(head -n 1 "$ASSET_DIR/$script")" == "#!/usr/bin/env bash" ]] || {
    echo "Standalone $script asset is not a shell script" >&2
    exit 1
  }
  bash -n "$ASSET_DIR/$script" || {
    echo "Standalone $script asset has invalid shell syntax" >&2
    exit 1
  }
  chmod 0755 "$ASSET_DIR/$script"
  [[ -x "$ASSET_DIR/$script" ]] || {
    echo "Standalone $script asset is not executable" >&2
    exit 1
  }
done

# RV-15：分发的 .sha256 必须与分发的脚本逐字节对应。仓库里这一对文件如果过期，
# 这里就会在发布前失败，而不是把一个校验不过的资产发出去。
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$ASSET_DIR" && sha256sum -c sforum-latest.sh.sha256)
else
  (cd "$ASSET_DIR" && shasum -a 256 -c sforum-latest.sh.sha256)
fi

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$ASSET_DIR" && sha256sum "${EXPECTED[@]}" > SHA256SUMS && sha256sum -c SHA256SUMS)
else
  (cd "$ASSET_DIR" && shasum -a 256 "${EXPECTED[@]}" > SHA256SUMS && shasum -a 256 -c SHA256SUMS)
fi

echo "Release asset matrix and checksums verified for SForum $VERSION"
