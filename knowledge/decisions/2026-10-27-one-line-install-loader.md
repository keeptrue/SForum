# Decision: One-Line Install Loader

## Status

Accepted on 2026-10-27.

## Context

The verified install entry was a 20-line `set -eu` subshell: create the
directory, download the bootstrap and `SHA256SUMS` into a temporary directory,
select the exact filename entry, verify the checksum and provenance, promote the
bootstrap, then execute it. That is correct and auditable, but it is not a
realistic first-run experience next to `get.docker.com` or `rustup`, and it had
to be repeated verbatim in `README.md` and both `deployment.md` handbooks.

`2026-08-18-verified-release-bootstrap.md` rejected piping a remote response
into a shell because the response would execute before its Release checksum or
GitHub provenance could be verified. `tests/validate-docs.mjs` enforced that
with a blanket ban on `curl ... | bash`.

The bootstrap cannot become the pipe target itself. It locates its install root
from `BASH_SOURCE`, and a piped shell has no file on disk to re-exec after the
self-refresh; its interactive wizard also reads stdin, which a pipe has already
consumed. Making it dual-mode would merge "bootstrap the directory" and "own the
install state machine" into one script.

## Decision

Publish `sforum-install.sh` as a tenth fixed-name Release asset and make it the
only shell entry that may run from a pipe.

- It does exactly four things: resolve an immutable tag, download that tag's
  `sforum-bootstrap.sh` and `SHA256SUMS`, verify the exact filename entry (plus
  optional `gh attestation verify` and an `SFORUM_INSTALL_SHA256` pin), and
  promote the bootstrap with `install -m 0755` before `exec`ing it.
- It carries no install logic and never modifies `.env.production`,
  `.deployrc`, `deploy/runtime/`, or data volumes. It reads only their existence
  and the recorded version, to choose install versus upgrade.
- The install root is the caller's working directory, defaulting to `./sforum`,
  because piped execution leaves `BASH_SOURCE` empty. Since
  `2026-10-27-interactive-install-directory.md` the loader confirms that
  directory interactively, shows its absolute path, and defaults to the current
  directory when that directory is already an installation.
- An existing installation with `.env.production` and a versioned `.deployrc`
  switches to `upgrade` automatically. A half-finished directory stops with a
  pointer to `sforum-latest.sh` or `deploy.sh` instead of guessing.
- Piped execution reconnects stdin to `/dev/tty` for the bootstrap. With no
  terminal it announces the fallback and continues with the recommended
  defaults (`--yes`).
- `sforum-bootstrap.sh` keeps its existing contract and is still forbidden as a
  pipe target. `tests/validate-docs.mjs` narrows the old blanket ban instead of
  dropping it: a `curl ... | bash` line must reference `sforum-install.sh`.
- The complete auditable sequence stays in both `one-click.md` handbooks, and
  the docs gate fails if any of its markers disappear.

The first layer — the loader's own bytes — is unverified, which is the same
tradeoff `get.docker.com` and `rustup` accept. The compensations are its small
auditable size, the `SFORUM_INSTALL_SHA256` pin, the bootstrap's own
re-verification, provenance attestation, and `sforum-latest.sh` for operators
who must verify the first layer too.

## Consequences

- The Release asset matrix grows from 9 to 10 and `SHA256SUMS` from 9 to 10
  entries; `finalize-release-assets.sh` and `release_assets_test.sh` pin both
  counts.
- Releases published before this change have no loader. Their documented
  auditable sequence still works, and the rolling `latest` entry gains the
  loader as soon as one release ships it.
- `sforum-latest.sh` keeps its distinct job: re-syncing the whole managed
  toolkit from a Release. The loader only refreshes the bootstrap.
- `deploy/scripts/install-loader_test.sh` becomes part of the release tooling
  gate, covering install, update, tampered and duplicated checksums, the pin,
  pinned and prerelease tags, half-finished directories, and the
  non-interactive fallback.
