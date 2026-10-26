#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export LANG=C

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION_INPUT="${1:-}"
VERSION="${VERSION_INPUT#v}"
COMMIT="${2:-}"
BUILD_TIME="${3:-}"
IMAGE_TAG="${4:-}"
TARGET_OS="${5:-}"
TARGET_ARCH="${6:-}"
OUTPUT_DIR="${7:-}"
REGISTRY="${SFORUM_REGISTRY:-ghcr.io/keeptrue}"

usage() {
  echo "Usage: build-release-assets.sh VERSION COMMIT BUILD_TIME IMAGE_TAG GOOS GOARCH OUTPUT_DIR" >&2
  exit 2
}

[[ $# -eq 7 ]] || usage
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || usage
[[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || usage
[[ -n "$BUILD_TIME" ]] || usage
[[ "$IMAGE_TAG" == "sha-$COMMIT" ]] || usage
case "$TARGET_OS" in
  linux|darwin) ;;
  *) usage ;;
esac
case "$TARGET_ARCH" in
  amd64|arm64) ;;
  *) usage ;;
esac

mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd)"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sforum-release-assets.XXXXXX")"
CONTAINERS=()

cleanup() {
  local container=""
  for container in "${CONTAINERS[@]-}"; do
    [[ -n "$container" ]] || continue
    docker rm -f "$container" >/dev/null 2>&1 || true
  done
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

write_build_identity() {
  local destination="$1"
  {
    echo "version=$VERSION"
    echo "commit=$COMMIT"
    echo "build_time=$BUILD_TIME"
    echo "goos=$TARGET_OS"
    echo "goarch=$TARGET_ARCH"
  } > "$destination"
}

verify_binary_target() {
  local binary="$1"
  local expected_os="$2"
  local expected_arch="$3"
  local metadata=""

  metadata="$(go version -m "$binary")"
  grep -Fq "GOOS=$expected_os" <<< "$metadata" || {
    echo "Binary target mismatch for $binary: expected GOOS=$expected_os" >&2
    exit 1
  }
  grep -Fq "GOARCH=$expected_arch" <<< "$metadata" || {
    echo "Binary target mismatch for $binary: expected GOARCH=$expected_arch" >&2
    exit 1
  }
}

archive_directory() {
  local directory="$1"
  local archive="$2"
  local base=""
  local parent=""

  base="$(basename "$directory")"
  parent="$(dirname "$directory")"
  if touch -d "$BUILD_TIME" "$directory" >/dev/null 2>&1; then
    find "$directory" -exec touch -d "$BUILD_TIME" {} +
  fi
  case "$archive" in
    *.tar.gz)
      if tar --version 2>/dev/null | grep -q 'GNU tar'; then
        tar -C "$parent" --sort=name --mtime="$BUILD_TIME" \
          --owner=0 --group=0 --numeric-owner -cf - "$base" | gzip -n > "$archive"
      else
        tar -C "$parent" -czf "$archive" "$base"
      fi
      ;;
    *)
      echo "Unsupported archive: $archive" >&2
      exit 2
      ;;
  esac
}

create_image_container() {
  local service="$1"
  local platform="linux/$TARGET_ARCH"
  local image="$REGISTRY/sforum-$service:$IMAGE_TAG"

  docker pull --platform "$platform" "$image"
  LAST_CONTAINER="$(docker create --platform "$platform" "$image")"
  CONTAINERS+=("$LAST_CONTAINER")
}

CLI_ROOT_NAME="sforum-cli_${VERSION}_${TARGET_OS}_${TARGET_ARCH}"
CLI_ROOT="$WORK_DIR/$CLI_ROOT_NAME"
CLI_BINARY_NAME="sforum"
CLI_ARCHIVE="$OUTPUT_DIR/$CLI_ROOT_NAME.tar.gz"

mkdir -p "$CLI_ROOT"
LDFLAGS="-s -w -X github.com/keeptrue/sforum/apps/api/version.Current=$VERSION -X github.com/keeptrue/sforum/apps/api/version.Commit=$COMMIT -X github.com/keeptrue/sforum/apps/api/version.BuildTime=$BUILD_TIME"
(
  cd "$ROOT_DIR/apps/api"
  CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" \
    go build -trimpath -buildvcs=false -ldflags="$LDFLAGS" \
    -o "$CLI_ROOT/$CLI_BINARY_NAME" ./cmd/sforum
)
verify_binary_target "$CLI_ROOT/$CLI_BINARY_NAME" "$TARGET_OS" "$TARGET_ARCH"
cp "$ROOT_DIR/LICENSE" "$CLI_ROOT/LICENSE"
write_build_identity "$CLI_ROOT/VERSION"
cat > "$CLI_ROOT/README.txt" <<EOF
SForum management CLI $VERSION

This archive contains the sforum developer and recovery CLI for
$TARGET_OS/$TARGET_ARCH. Run "sforum --version" (or "sforum.exe --version")
to inspect its embedded release identity.

Documentation: https://github.com/keeptrue/SForum/tree/main/docs
EOF
archive_directory "$CLI_ROOT" "$CLI_ARCHIVE"

if [[ "$TARGET_OS" != "linux" ]]; then
  echo "Created release asset: $CLI_ARCHIVE"
  exit 0
fi

SERVER_ROOT_NAME="sforum-server_${VERSION}_linux_${TARGET_ARCH}"
SERVER_ROOT="$WORK_DIR/$SERVER_ROOT_NAME"
SERVER_ARCHIVE="$OUTPUT_DIR/$SERVER_ROOT_NAME.tar.gz"
mkdir -p "$SERVER_ROOT/bin" "$SERVER_ROOT/extensions" "$SERVER_ROOT/storage/extensions"

create_image_container api
docker cp "$LAST_CONTAINER:/usr/local/bin/sforum-api" "$SERVER_ROOT/bin/sforum-api"
docker cp "$LAST_CONTAINER:/app/extensions/builtin" "$SERVER_ROOT/extensions/"

create_image_container migrate
docker cp "$LAST_CONTAINER:/usr/local/bin/sforum-migrate" "$SERVER_ROOT/bin/sforum-migrate"

cp "$CLI_ROOT/$CLI_BINARY_NAME" "$SERVER_ROOT/bin/sforum"
chmod +x "$SERVER_ROOT/bin/sforum-api" "$SERVER_ROOT/bin/sforum-migrate" \
  "$SERVER_ROOT/bin/sforum"

for binary in sforum-api sforum-migrate sforum; do
  verify_binary_target "$SERVER_ROOT/bin/$binary" linux "$TARGET_ARCH"
done

PLUGIN_COUNT="$(find "$SERVER_ROOT/extensions/builtin/plugins" -path '*/backend/plugin' -type f | wc -l | tr -d ' ')"
if [[ "$PLUGIN_COUNT" -eq 0 ]]; then
  echo "No protected built-in plugin binaries were extracted from the API candidate" >&2
  exit 1
fi

cp "$ROOT_DIR/LICENSE" "$SERVER_ROOT/LICENSE"
write_build_identity "$SERVER_ROOT/VERSION"
cat > "$SERVER_ROOT/server.env.example" <<'EOF'
BUILTIN_EXTENSION_ROOT=./extensions/builtin
EXTENSION_ROOT=./storage/extensions
EOF
cat > "$SERVER_ROOT/README.txt" <<EOF
SForum backend runtime bundle $VERSION for linux/$TARGET_ARCH

This archive contains sforum-api, sforum-migrate, the sforum management CLI,
and the exact protected built-ins extracted from the scanned release candidate
images. The API owns background job processing. Run the processes from this
archive's root and set BUILTIN_EXTENSION_ROOT and EXTENSION_ROOT as shown in
server.env.example.

The Nuxt web runtime, PostgreSQL, and Redis are not included. Docker Compose
with the published SForum images remains the recommended production deployment.
EOF
archive_directory "$SERVER_ROOT" "$SERVER_ARCHIVE"

echo "Created release assets:"
echo "  $CLI_ARCHIVE"
echo "  $SERVER_ARCHIVE"
