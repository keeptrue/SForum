# Knowledge Base

Project memory for future sessions and contributors.

## Start here

1. Human product/dev handbooks: `docs/zh-CN/` or `docs/en-US/` (not this tree)
2. `index.md` — current handoffs, short project state, navigation
3. `modules/<area>.md` — living feature notes
4. `sessions/` — hot handoffs only
5. `plans/` — active task books (see `plans/README.md` for status)
6. `decisions/` — ADRs (architecture choices that must not be rediscovered)

## Layout

| Path | Purpose |
| --- | --- |
| `index.md` | Entry point; keep slim |
| `modules/` | Per-domain living status |
| `sessions/` | Actionable recent handoffs |
| `sessions/archive/` | Cold historical handoffs |
| `plans/` | Current implementation checklists and progress ledgers |
| `plans/archive/` | Completed, cancelled, and superseded task books |
| `decisions/` | Accepted / superseded decisions |
| `glossary.md` | Shared terms |
| `archive/` | Stale audits, legacy comparisons, and historical research |
| `reports/` | Point-in-time scans |
| `runbooks/` | Reproducible operational procedures with their expected signals |

## Hygiene

- Prefer updating a module note over appending long changelog text to `index.md`.
- Keep `index.md` under 150 lines and limited to active workstreams/current
  state; completed feature summaries belong in module notes or cold history.
- Hot sessions stay few; move intermediate checkpoints to `sessions/archive/YYYY-MM/`.
- Every plan file needs a clear **Status** line.
- Superseded decisions keep their file; mark `Superseded by:` at the top.
- **Current fact vs. superseded claim.** Product and implementation docs (module
  notes, `docs/`) state only what is true **now**. A claim that was overturned lives
  only in a change ledger (for example `reports/*-superseded-*.md`) and must stay
  **verbatim quotable** there: printing a wrong identifier in a module note turns it
  into a search hit and pollutes the facts, while omitting it from the ledger
  destroys the ability to refute it. A ledger entry quoting a wrong identifier must
  sit inside backticks and be followed immediately by a negation ("never existed;
  the implementation is X").
- **Zero-hit claims are commands, not prose.** A "no occurrences remain" statement
  written as prose decays into an opinion. Record the command and its expected
  output, and date the conclusion ("closed as of YYYY-MM-DD"), so the next session
  can re-run it and see the claim re-open if someone reverts the fix.
- **Zero-hit claims have an upgrade obligation.** A fresh "observed at time T" claim
  must be upgraded to commit-level evidence once it lands (`git log -S"<symbol>"
  --oneline` plus `git grep "<symbol>" <rev> -- '*.go'`). Only version history can
  distinguish the three cases that prose keeps conflating: **never existed**, **existed
  and was removed**, **existed only in an uncommitted worktree**. Until then, `git log -S`
  is empty evidence — it can only adjudicate committed history — so an observation is
  the only legal form, not a weaker one. Left un-upgraded, even a rigorous
  observation becomes hearsay within a release or two. The two rules form one chain:
  **write the command first, then replace it with commit-level evidence.**
- **A command gives a snapshot, not a history.** `grep` finding nothing proves "not
  now", never "never existed". Claiming "never existed" requires scanning the whole
  history; claiming "removed" requires showing both sides of the removal.
- **Re-run the lookup for every line number and function location you cite.** Line
  numbers are the most brittle citation in this tree: a function that moves to its own
  file leaves the old `file.go:NNN` pointing at something unrelated, and nothing
  fails. Spend the two seconds to `rg` the symbol at write time; never copy a
  location out of a report, a plan, or an earlier doc without re-checking it.
