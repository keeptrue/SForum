# Decision: Interactive Install Directory

## Status

Accepted on 2026-10-27. Amends
`2026-10-27-one-line-install-loader.md`; it does not replace it.

## Context

The one-line loader hard-coded the install root to `$PWD/sforum` and never said
so. Three problems followed:

- An operator with a separate data disk, a multi-instance host, or an existing
  directory tree had to know `-d DIR` before the first run, or discover the
  layout afterwards.
- Nothing printed where the installation actually landed. `deploy.sh` printed
  access URLs only, so "where is my `.env.production`?" was answered by
  `find`.
- Running `curl ... | bash` from inside an installation created a nested
  `./sforum/sforum`, because the default never considered the current
  directory.

Piped execution still leaves `BASH_SOURCE` empty, so the caller's working
directory remains the only anchor. That is a reason to confirm the directory,
not a reason to keep it implicit.

## Decision

- `sforum-install.sh` asks once for the install directory, with the absolute
  default in the prompt: `$PWD/sforum`, or `$PWD` when the current directory is
  already an installation. Enter accepts the default.
- The resolved absolute path is echoed before any network work, in every mode,
  and the bootstrap prints it again before handing off to `deploy.sh` or
  `upgrade.sh`.
- Only the loader asks. `sforum-bootstrap.sh`, `sforum-latest.sh`, and
  `deploy.sh` keep "install into the directory the script lives in" and only
  report that absolute path. Prompting there would require copying the whole
  toolkit into a second directory before executing it, which is a new failure
  surface for no real gain.
- `-d DIR` and `SFORUM_INSTALL_DIR` keep their meaning and skip the prompt.
  `--yes`, `--defaults`, `SFORUM_NONINTERACTIVE=1`, and a missing terminal skip
  the prompt and use the default; `SFORUM_NONINTERACTIVE=1` additionally
  forwards `--yes` to the bootstrap so automation never stops at the channel
  prompt or the configuration wizard.
- Validation loops instead of aborting: the filesystem root, an existing
  regular file, and an unwritable location are refused with the question
  repeated; a non-empty directory that is not an installation asks once more
  with `[y/N]` and returns to the question when declined.
- Path handling stays dependency-free: lexical absolute resolution plus
  `pwd -P` after creation. `realpath` is not available on every supported
  macOS, and symlinked `/home` or `/var` must still be reported physically.
- The loader still owns no install state. The prompt only selects the directory
  the verified bootstrap is promoted into; `.env.production` and `.deployrc`
  are read for existence and the recorded version (to choose install versus
  upgrade and to label the prompt) and are never modified.
- `.deployrc` gains `install_dir=`. `deploy.sh` and `upgrade.sh` print the
  install directory, configuration file, and backup directory on success and on
  `status`; a recorded directory that differs from the current one is reported
  instead of silently ignored.

## Consequences

- `deploy/scripts/install-loader_test.sh` grows a pseudo-terminal matrix
  (default, relative, absolute, refused root, non-empty confirm and decline,
  in-place install, `--yes`, `SFORUM_NONINTERACTIVE`). Non-interactive cases pin
  `SFORUM_NONINTERACTIVE=1` so no test can block on a developer's terminal.
- `tests/validate-docs.mjs` requires `SFORUM_NONINTERACTIVE` in both one-click
  handbooks, so the non-interactive contract cannot silently disappear.
- `sforum-latest.sh` prints the install directory too. It is repository tooling,
  not a Release asset, but its published SHA-256 must be regenerated.
- `upgrade.sh` rewrites `.deployrc` wholesale, so it must persist `install_dir`
  as well; otherwise the key disappears after the first update.
- The loader's audited surface grows by roughly 150 lines of prompting and path
  handling. That is the price of not guessing; it still cannot touch install
  state, and every branch has a behaviour test.
