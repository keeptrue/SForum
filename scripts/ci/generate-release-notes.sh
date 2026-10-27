#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2129
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "Usage: $0 <release-tag> <output-file>" >&2
  exit 2
fi

release_tag="$1"
output_file="$2"
if [[ ! "$release_tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
  echo "Invalid release tag: $release_tag" >&2
  exit 2
fi
git rev-parse --verify --quiet "refs/tags/$release_tag^{commit}" >/dev/null || {
  echo "Release tag does not exist: $release_tag" >&2
  exit 1
}

repository_url="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-}"
if [ -z "${GITHUB_REPOSITORY:-}" ]; then
  remote_url="$(git remote get-url origin 2>/dev/null || true)"
  case "$remote_url" in
    https://github.com/*.git) repository_url="${remote_url%.git}" ;;
    https://github.com/*) repository_url="$remote_url" ;;
    git@github.com:*) repository_url="https://github.com/${remote_url#git@github.com:}"; repository_url="${repository_url%.git}" ;;
    *) repository_url="https://github.com/keeptrue/SForum" ;;
  esac
fi

release_commit="$(git rev-parse "refs/tags/$release_tag^{commit}")"
# 稳定版用 README 里的简短一键入口（releases/latest/download）；预发布不能这么写，
# 因为 `latest` 永远指向最新稳定版，会把预发布说明变成「装稳定版」的入口。
if [[ "$release_tag" == *-* ]]; then
  install_url="$repository_url/releases/download/$release_tag/sforum-install.sh"
  install_label="this release"
else
  install_url="$repository_url/releases/latest/download/sforum-install.sh"
  install_label="latest stable, same one-liner as the README"
fi
previous_tag="$(git describe --tags --abbrev=0 "$release_commit^" 2>/dev/null || true)"
tag_notes="$(git for-each-ref --format='%(contents:body)' "refs/tags/$release_tag")"
temp_file="$(mktemp "${TMPDIR:-/tmp}/sforum-release-notes.XXXXXX")"
trap 'rm -f "$temp_file"' EXIT

printf '> Maintainable, plugin-first open-source forum framework.\n\n' >> "$temp_file"
range="$release_tag"
if [ -n "$previous_tag" ]; then
  range="$previous_tag..$release_tag"
fi
if [ -n "${tag_notes//[[:space:]]/}" ]; then
  printf '%s\n' "$tag_notes" >> "$temp_file"
else
  commit_count=0
  while IFS= read -r subject; do
    [ -n "$subject" ] || continue
    subject="$(printf '%s' "$subject" | sed -E 's/^(feat|fix|docs|test|refactor|perf|build|ci|chore)(\([^)]*\))?!?:[[:space:]]*//')"
    printf -- '- %s\n' "$subject" >> "$temp_file"
    commit_count=$((commit_count + 1))
  done < <(git log --format='%s' "$range")
  if [ "$commit_count" -eq 0 ]; then
    subject="$(git show -s --format=%s "$release_commit")"
    printf -- '- %s\n' "$subject" >> "$temp_file"
  fi
fi

printf '\n---\n\n## Installation\n\n**Docker images:**\n\n```bash\n' >> "$temp_file"
for image in api migrate web; do
  printf 'docker pull ghcr.io/keeptrue/sforum-%s:%s\n' "$image" "$release_tag" >> "$temp_file"
done
printf '```\n\n**One-line install (%s):**\n\n```bash\n' "$install_label" >> "$temp_file"
printf 'curl -fsSL %s | bash\n' "$install_url" >> "$temp_file"
printf '```\n\n**One-line update (does not trust the local bootstrap):**\n\n```bash\n' >> "$temp_file"
printf 'curl -fsSL %s | bash -s -- upgrade\n' "$install_url" >> "$temp_file"
printf '```\n\n**Update an existing installation with a verified bootstrap:**\n\n```bash\n(\n  set -eu\n  cd /path/to/sforum\n' >> "$temp_file"
printf '  bootstrap_dir="$(mktemp -d .sforum-bootstrap.XXXXXX)"\n' >> "$temp_file"
printf "  trap 'rm -rf \"\$bootstrap_dir\"' EXIT HUP INT TERM\n" >> "$temp_file"
printf '  curl -fsSLo "$bootstrap_dir/sforum-bootstrap.sh" %s/releases/download/%s/sforum-bootstrap.sh\n' "$repository_url" "$release_tag" >> "$temp_file"
printf '  curl -fsSLo "$bootstrap_dir/SHA256SUMS" %s/releases/download/%s/SHA256SUMS\n' "$repository_url" "$release_tag" >> "$temp_file"
printf '  (\n    cd "$bootstrap_dir"\n' >> "$temp_file"
printf "    awk '\$2 == \"sforum-bootstrap.sh\" { print }' SHA256SUMS > sforum-bootstrap.sha256\n" >> "$temp_file"
printf "    test \"\$(wc -l < sforum-bootstrap.sha256 | tr -d '[:space:]')\" = 1\n" >> "$temp_file"
printf '    if command -v sha256sum >/dev/null 2>&1; then\n      sha256sum -c sforum-bootstrap.sha256\n    else\n      shasum -a 256 -c sforum-bootstrap.sha256\n    fi\n' >> "$temp_file"
printf '    if command -v gh >/dev/null 2>&1; then\n      gh attestation verify sforum-bootstrap.sh --repo %s\n    fi\n' "${repository_url#https://github.com/}" >> "$temp_file"
printf '  )\n  install -m 0755 "$bootstrap_dir/sforum-bootstrap.sh" ./sforum-bootstrap.sh\n  rm -rf "$bootstrap_dir"\n  trap - EXIT HUP INT TERM\n' >> "$temp_file"
printf '  ./sforum-bootstrap.sh upgrade --version %s\n)\n```\n\n' "$release_tag" >> "$temp_file"
printf '**Fresh install with the verified deploy bundle:**\n\n```bash\n(\n  set -eu\n  mkdir -p sforum\n  cd sforum\n' >> "$temp_file"
printf '  curl -fsSLo sforum-deploy.tar.gz %s/releases/download/%s/sforum-deploy.tar.gz\n' "$repository_url" "$release_tag" >> "$temp_file"
printf '  curl -fsSLo SHA256SUMS %s/releases/download/%s/SHA256SUMS\n' "$repository_url" "$release_tag" >> "$temp_file"
printf "  awk '\$2 == \"sforum-deploy.tar.gz\" { print }' SHA256SUMS > sforum-deploy.sha256\n" >> "$temp_file"
printf "  test \"\$(wc -l < sforum-deploy.sha256 | tr -d '[:space:]')\" = 1\n" >> "$temp_file"
printf '  if command -v sha256sum >/dev/null 2>&1; then\n    sha256sum -c sforum-deploy.sha256\n  else\n    shasum -a 256 -c sforum-deploy.sha256\n  fi\n' >> "$temp_file"
printf '  if command -v gh >/dev/null 2>&1; then\n    gh attestation verify sforum-deploy.tar.gz --repo %s\n  fi\n' "${repository_url#https://github.com/}" >> "$temp_file"
printf '  tar -xzf sforum-deploy.tar.gz --strip-components=1\n' >> "$temp_file"
printf '  ./sforum-bootstrap.sh install --yes --version %s\n)\n```\n\n' "$release_tag" >> "$temp_file"
printf '**Manual download:** Download the matching Linux or macOS archive from the assets below.\n\n' >> "$temp_file"

printf '## Documentation\n\n' >> "$temp_file"
printf -- '- [GitHub Repository](%s)\n' "$repository_url" >> "$temp_file"
printf -- '- [中文部署文档](%s/blob/%s/docs/zh-CN/deployment.md)\n' "$repository_url" "$release_tag" >> "$temp_file"
printf -- '- [English Deployment Guide](%s/blob/%s/docs/en-US/deployment.md)\n' "$repository_url" "$release_tag" >> "$temp_file"
printf -- '- **完整变更记录 / Full Changelog**: ' >> "$temp_file"
if [ -n "$previous_tag" ]; then
  printf '[`%s...%s`](%s/compare/%s...%s)\n' \
    "$previous_tag" "$release_tag" "$repository_url" "$previous_tag" "$release_tag" >> "$temp_file"
else
  printf '[`%s`](%s/commits/%s)\n' "$release_tag" "$repository_url" "$release_tag" >> "$temp_file"
fi

mkdir -p "$(dirname "$output_file")"
mv "$temp_file" "$output_file"
trap - EXIT
