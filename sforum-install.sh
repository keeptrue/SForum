#!/usr/bin/env bash
# sforum-install.sh - one-line install/update entry for SForum.
#
# Resolves an immutable Release tag, downloads and verifies that tag's
# sforum-bootstrap.sh, promotes it into the install directory, then execs it.
# Installation state still belongs to sforum-bootstrap.sh, deploy.sh, and
# upgrade.sh. This loader never touches .env.production, .deployrc,
# deploy/runtime/, or data volumes.
#
#   curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash
#   curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash -s -- upgrade
#
# Piping into bash means the loader's own bytes are not checksummed. That is the
# same first-layer tradeoff as get.docker.com and rustup: this file is small
# enough to audit, SFORUM_INSTALL_SHA256 pins the bootstrap it will accept, the
# bootstrap re-verifies itself, and gh attestation covers provenance. Use
# ./sforum-latest.sh when even the first layer must be verified.
set -euo pipefail
export LC_ALL=C

REPOSITORY="${SFORUM_RELEASE_REPOSITORY:-keeptrue/SForum}"
DOWNLOAD_BASE="${SFORUM_RELEASE_DOWNLOAD_BASE_URL:-https://github.com/$REPOSITORY/releases/download}"
API_ROOT="https://api.github.com/repos/$REPOSITORY/releases"

# Piped execution leaves BASH_SOURCE empty, so the caller's working directory is
# the only meaningful anchor for the default install location.
WORK_DIR="$PWD"
INSTALL_DIR="${SFORUM_INSTALL_DIR:-$WORK_DIR/sforum}"
ACTION=""
ACTION_EXPLICIT=false
TARGET_VERSION=""
CHANNEL="stable"
PASSTHROUGH=()
STAGE=""
BOOTSTRAP=""

usage() {
  cat <<'EOF'
Usage: curl -fsSL .../sforum-install.sh | bash -s -- [install|upgrade] [options]

Resolve an immutable Release tag, verify that tag's sforum-bootstrap.sh, then
hand off to it. The bootstrap refreshes the complete matching toolkit before
running the install or update state machine.

Options:
  install              Fresh install (default when the directory has no state)
  upgrade              Update an existing installation (default when installed)
  -d, --dir DIR        Install directory (default: ./sforum)
  --version VERSION    Immutable target tag, or latest (default: latest)
  --channel CHANNEL    stable (default) or prerelease
  --lang zh|en         Installer language (install only)
  --yes, --defaults    Skip supported confirmations
  --bootstrap          Confirm the legacy ingress conversion (upgrade only)
  -h, --help           Show this help

Environment:
  SFORUM_INSTALL_DIR              Same as --dir
  SFORUM_INSTALL_SHA256           Require this exact SHA-256 for the downloaded
                                  sforum-bootstrap.sh (second check beyond
                                  SHA256SUMS)
  SFORUM_RELEASE_REPOSITORY       Override the repository (keeptrue/SForum)
  SFORUM_RELEASE_DOWNLOAD_BASE_URL Override the release download base
  SFORUM_LATEST_RELEASE_API_URL   Override the stable release lookup
  SFORUM_RELEASES_API_URL         Override the prerelease lookup
  SFORUM_SKIP_ATTESTATION=true    Skip gh build provenance verification

Without a terminal the loader continues with the recommended defaults and tells
you so. A directory with .env.production but no version in .deployrc is a
half-finished state and is left to ./sforum-latest.sh or manual repair.
EOF
}

die() {
  printf 'sforum-install: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  [ -z "$STAGE" ] || rm -rf "$STAGE"
}

passthrough_has() {
  local needle="$1" item
  for item in ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}; do
    [ "$item" = "$needle" ] && return 0
  done
  return 1
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      install|upgrade)
        ACTION="$1"
        ACTION_EXPLICIT=true
        shift
        ;;
      -d|--dir)
        [ "$#" -ge 2 ] || die "--dir needs a value"
        INSTALL_DIR="$2"
        shift 2
        ;;
      --dir=*) INSTALL_DIR="${1#*=}"; shift ;;
      --version)
        [ "$#" -ge 2 ] || die "--version needs a value"
        [ -z "$TARGET_VERSION" ] || die "only one target version may be given"
        TARGET_VERSION="$2"
        shift 2
        ;;
      --version=*)
        [ -z "$TARGET_VERSION" ] || die "only one target version may be given"
        TARGET_VERSION="${1#*=}"
        shift
        ;;
      --channel)
        [ "$#" -ge 2 ] || die "--channel needs a value"
        CHANNEL="$2"
        shift 2
        ;;
      --channel=*) CHANNEL="${1#*=}"; shift ;;
      --lang)
        [ "$#" -ge 2 ] || die "--lang needs a value"
        PASSTHROUGH+=("$1" "$2")
        shift 2
        ;;
      --lang=*) PASSTHROUGH+=("$1"); shift ;;
      --yes|--defaults) PASSTHROUGH+=("$1"); shift ;;
      --bootstrap) PASSTHROUGH+=("$1"); shift ;;
      -h|--help) usage; exit 0 ;;
      -*) die "unknown option: $1" ;;
      *) die "unexpected argument: $1" ;;
    esac
  done
  [ "$CHANNEL" = stable ] || [ "$CHANNEL" = prerelease ] || \
    die "--channel must be stable or prerelease"
}

normalize_version() {
  local tag="$1"
  case "$tag" in
    v[0-9]*) ;;
    [0-9]*) tag="v$tag" ;;
  esac
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] || \
    die "version must look like v3.0.0 or v3.0.0-alpha.13"
  printf '%s' "$tag"
}

resolve_version() {
  local api_url response resolved
  if [ -n "$TARGET_VERSION" ]; then
    normalize_version "$TARGET_VERSION"
    return 0
  fi

  if [ "$CHANNEL" = stable ]; then
    api_url="${SFORUM_LATEST_RELEASE_API_URL:-$API_ROOT/latest}"
  else
    api_url="${SFORUM_RELEASES_API_URL:-$API_ROOT?per_page=1}"
  fi

  response="$(curl -fsSL \
    --connect-timeout 10 \
    --max-time 30 \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2022-11-28' \
    -H 'User-Agent: SForum-install' \
    "$api_url")" || \
    die "could not query the latest $CHANNEL Release; check the network, set a proxy, or pass --version"

  resolved="$(printf '%s\n' "$response" \
    | sed -n 's/^.*"tag_name":[[:space:]]*"\([^"]*\)".*$/\1/p' \
    | sed -n '1p')"
  [ -n "$resolved" ] || die "GitHub returned no matching published Release"
  normalize_version "$resolved"
}

prepare_install_dir() {
  case "$INSTALL_DIR" in
    /*) ;;
    *) INSTALL_DIR="$WORK_DIR/$INSTALL_DIR" ;;
  esac
  [ ! -e "$INSTALL_DIR" ] || [ -d "$INSTALL_DIR" ] || \
    die "install path exists and is not a directory: $INSTALL_DIR"
  mkdir -p "$INSTALL_DIR" || die "could not create install directory: $INSTALL_DIR"
}

detect_action() {
  if [ -f "$INSTALL_DIR/.env.production" ]; then
    if [ -f "$INSTALL_DIR/.deployrc" ] && \
       grep -Eq '^version=v[0-9]+\.[0-9]+\.[0-9]+' "$INSTALL_DIR/.deployrc"; then
      ACTION=upgrade
    else
      ACTION=repair
    fi
  elif [ -f "$INSTALL_DIR/.deployrc" ]; then
    ACTION=incomplete
  else
    ACTION=install
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

stage_and_verify() {
  local checksum_file expected_hash actual_hash

  STAGE="$(mktemp -d "$INSTALL_DIR/.sforum-install.XXXXXX")" || \
    die "could not create a staging directory in $INSTALL_DIR"

  curl -fsSLo "$STAGE/sforum-bootstrap.sh" \
    --connect-timeout 10 \
    --max-time 120 \
    "$DOWNLOAD_BASE/$TARGET_VERSION/sforum-bootstrap.sh" || \
    die "could not download sforum-bootstrap.sh for $TARGET_VERSION"
  curl -fsSLo "$STAGE/SHA256SUMS" \
    --connect-timeout 10 \
    --max-time 120 \
    "$DOWNLOAD_BASE/$TARGET_VERSION/SHA256SUMS" || \
    die "could not download SHA256SUMS for $TARGET_VERSION"

  # Compare explicitly instead of running `sha256sum -c` inside a subshell: a
  # `( ... ) || die` list suppresses errexit inside the subshell, so an
  # assertion there would silently pass on a duplicated entry.
  checksum_file="$STAGE/sforum-bootstrap.sha256"
  awk '$2 == "sforum-bootstrap.sh" { print }' "$STAGE/SHA256SUMS" > "$checksum_file"
  [ "$(wc -l < "$checksum_file" | tr -d '[:space:]')" = 1 ] || \
    die "sforum-bootstrap.sh checksum verification failed for $TARGET_VERSION (SHA256SUMS must contain exactly one sforum-bootstrap.sh entry)"

  expected_hash="$(awk '{print $1}' "$checksum_file")"
  actual_hash="$(sha256_of "$STAGE/sforum-bootstrap.sh")"
  [ "$actual_hash" = "$expected_hash" ] || \
    die "sforum-bootstrap.sh checksum verification failed for $TARGET_VERSION"

  if [ -n "${SFORUM_INSTALL_SHA256:-}" ]; then
    [ "$actual_hash" = "$(printf '%s' "$SFORUM_INSTALL_SHA256" | tr 'A-Z' 'a-z')" ] || \
      die "sforum-bootstrap.sh does not match SFORUM_INSTALL_SHA256"
  fi

  bash -n "$STAGE/sforum-bootstrap.sh" || \
    die "downloaded sforum-bootstrap.sh has invalid shell syntax"

  if [ "${SFORUM_SKIP_ATTESTATION:-false}" != true ] && command -v gh >/dev/null 2>&1; then
    if gh attestation --help >/dev/null 2>&1; then
      gh attestation verify "$STAGE/sforum-bootstrap.sh" --repo "$REPOSITORY" >/dev/null || \
        die "provenance verification failed for sforum-bootstrap.sh"
    else
      # Match sforum-latest.sh: an old gh must not fail the install, but the
      # bootstrap performs the same check, so skip it there too.
      export SFORUM_SKIP_ATTESTATION=true
      printf 'notice: local gh lacks attestation support; SHA-256 already verified\n' >&2
    fi
  fi
}

promote() {
  install -m 0755 "$STAGE/sforum-bootstrap.sh" "$INSTALL_DIR/sforum-bootstrap.sh" || \
    die "could not promote sforum-bootstrap.sh into $INSTALL_DIR"
  BOOTSTRAP="$INSTALL_DIR/sforum-bootstrap.sh"
  rm -rf "$STAGE"
  STAGE=""
}

run_bootstrap() {
  local args=("$ACTION" --version "$TARGET_VERSION")
  args+=(${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"})

  if [ -t 0 ]; then
    exec "$BOOTSTRAP" "${args[@]}"
  fi
  if ( : </dev/tty ) 2>/dev/null; then
    exec "$BOOTSTRAP" "${args[@]}" </dev/tty
  fi
  if ! passthrough_has --yes && ! passthrough_has --defaults; then
    printf 'notice: no interactive terminal available; continuing with recommended defaults (--yes)\n' >&2
    args+=(--yes)
  fi
  exec "$BOOTSTRAP" "${args[@]}"
}

main() {
  trap cleanup EXIT
  trap 'exit 130' HUP INT TERM

  parse_args "$@"

  printf 'SForum install loader: resolving %s release\n' "$CHANNEL"

  prepare_install_dir
  TARGET_VERSION="$(resolve_version)"
  printf 'Target release: %s\n' "$TARGET_VERSION"

  if [ "$ACTION_EXPLICIT" = false ]; then
    detect_action
  fi

  case "$ACTION" in
    install|upgrade) ;;
    repair)
      die "$INSTALL_DIR has .env.production but no version in .deployrc; run ./sforum-latest.sh from there to repair the toolkit, or ./deploy.sh --action deploy --version $TARGET_VERSION"
      ;;
    incomplete)
      die "$INSTALL_DIR has .deployrc but no .env.production; repair the directory by hand before installing"
      ;;
    *) die "unknown action: $ACTION" ;;
  esac

  if [ "$ACTION" = upgrade ] && passthrough_has --lang; then
    die "--lang only applies to install"
  fi
  if [ "$ACTION" != upgrade ] && passthrough_has --bootstrap; then
    die "--bootstrap only applies to upgrade"
  fi

  stage_and_verify
  promote

  printf 'Verified sforum-bootstrap.sh for %s; handing off to %s\n' \
    "$TARGET_VERSION" "$ACTION"
  run_bootstrap
}

main "$@"
