# Trust levels

[← Usage](./README.md)

> **Status: live (verified 2026-11-11).** The tier ladder (1–8 tiers, 5 by default), the
> threshold policy, the load-time merge, the product flag, the admin page
> ("Forum → Trust levels", with the Overview / Tiers / Thresholds / Capability unlocks
> tabs), the public level query endpoint, and the profile level card are all implemented
> and wired: **saving immediately changes tier evaluation and per-tier limits**. Earned
> capability unlocking is off by default and must be configured per tier by the operator
> — see "Turning on earned capabilities".
> **Two more keys are gated by the system baseline**: off at TL0 and on from TL1 on a
> default install — see "Two keys are level-gated".

## In one sentence

Role permissions answer "what did an admin grant you"; trust levels answer "how
much have you done here". The latter only unlocks permissions that **already
exist** — it never invents new capabilities. **The one subtraction**: during TL0 the
system withholds two baseline keys (create topics, upload attachments) and restores
them from TL1 on — no role is edited; see "Two keys are level-gated".

## What it is not

- **Not a second permission system.** Authorization still has exactly one entry
  point (`Can()`); trust levels are merged in at load time as an extra source, not
  a parallel check.
- **Not badges.** Badges are display only and never grant capability; trust levels
  are the inverse — capability only, never a badge.
- **Not an approval workflow.** No nominations, no manual promotion. Pure rules,
  pure computation.

## The ladder: 1–8 tiers, 5 by default

**TL0 is fixed.** It is the newcomer tier: it is not listed under Tiers and cannot be
renamed or retired. Everything from TL1 up is configurable; a default install enables 4
of them (5 tiers in total), and you can add or remove tiers as long as the total stays
between 1 and 8.

| Tier | Default name | Role |
| --- | --- | --- |
| TL0 | Newcomer | Just signed up; identical to today's newcomer limits |
| TL1 | Member | Past the trust window |
| TL2 | Resident | Consistent output |
| TL3 | Veteran | Long-term contributor |
| TL4 | Leader | Core of the community |

The table shows a **default install**. Tiers can be added, inserted, retired, renamed and
given new icons — see "Adding, reducing, and reordering tiers" and "Naming tiers and
choosing icons".

### Default thresholds

A tier is reached only when **all** its metrics are met; `0` means this metric does
not participate for this level.

**The actual numbers are whatever Admin → Forum → Trust levels shows right now.** That page
has a "Restore recommended defaults" button — click it to see the built-in values.
This page does not copy the numbers: thresholds are configurable, a copy here will
eventually diverge from what the site uses, and nobody notices the divergence.

TL0 does not participate in evaluation; its limits come from the seven newcomer
keys (next section).

**Account-age gate**: inside the "newcomer trust window" a user can reach at most
TL1 — enough activity leaves TL0 early, but never skips past TL1. That window is the
same value as the newcomer trust window.

**About limits**: site-default posting cooldowns and daily caps are `0` (no extra
restriction), so there is **no observable limit difference** between TL1 and the tiers
above it today. "Higher tiers skip cooldowns" is not true in this codebase and is not
written here.

## Adding, reducing, and reordering tiers

Path: **Admin → Forum → Trust levels → the "Tiers" tab**. The page shows
"{count} of {max} tiers active"; `{max}` is 8, so the number of tiers must stay between
1 and 8.

- **Adding**: click **"Add a tier"** to append one, or **"Insert a tier before this one"**
  to place it in the middle; **"Move up"** / **"Move down"** reorder afterwards.
- **Defaults of a new tier**: thresholds start at `0` (`0` means this metric does not
  participate for this level); limits are left empty to inherit the site defaults; the unlock
  list starts empty.
- **Inserting never changes what existing tiers already have configured.** Thresholds,
  limits and unlock choices follow the tier itself, not its position: after you insert a
  tier in front of one, that tier is still the same tier — only its number changed (the
  TL numbers shift with it).
- **Reducing the number of tiers is not deleting a tier.** There is no hard delete; you
  retire a tier instead — see the next section.
- **After saving, everyone is evaluated against the new ladder.** Levels are recomputed on
  every request, so inserting or retiring a tier needs no data migration, no backfill and
  nothing to wait for.

## Retiring and restoring tiers

- **Retiring is soft**: click **"Retire"** on a tier and it shows as **"Retired"**. Its
  thresholds, limits and unlock choices are **kept as they are**; evaluation and display
  skip it; **"Restore"** puts it back in its original position.
- **Retiring can take capabilities away.** The confirmation before saving is
  **"Retire “{name}”?"** and lists **"Capabilities that may be withdrawn: {keys}"** — that
  is what the tier contributes and what may be withdrawn from users. **If other active
  tiers already grant everything this tier adds, the dialog says "no capability is
  expected to be withdrawn"**, so it is only a change of order and nobody loses anything.
- **The last tier cannot be retired.** The page states "At least one tier must stay active;
  the last one cannot be retired", and the **"Retire" button is disabled** — you are not
  told only after saving.
- With only one tier left, the newcomer trust window no longer makes any difference (there
  is only one tier to reach), and the admin page says so.
- Retiring and restoring only edit the form: **nothing is written until you save** with the
  page footer.

## Naming tiers and choosing icons

- Every tier has two names, **"Chinese name"** and **"English name"**, both free text.
  **What you type is what is stored.** **"Name suggestions"** only fills a suggested name
  in; you can still edit it or clear it afterwards.
- **Display fallback**: the current language's name → the other language's name → the
  built-in default name (built-in tiers only) → `TL{number}`. If the English name is empty,
  the English site shows the Chinese name, and the admin page tells you.
- **Icons**: click **"Icon"** and pick from the allow-list the admin page offers. If you
  pick none, the default sequence applies by position — the page states "Without an
  explicit icon, the default sequence is used by position: {icons}" and lists the actual
  sequence. This page does not copy that list (it would drift); read it off the page. On a
  default site the first three are sprout, leaf and house.
- Renaming a tier or changing its icon **does not trigger re-evaluation and does not change
  any permission**: name and icon are presentation, not identity.

## What you can tune today: newcomer limits (seven keys)

Path: **Admin → Settings → the "Newcomers" tab** (one of the fixed tabs on the
settings page; seedling icon).

This tab is all of TL0 today, and it stays the single source of TL0 values once
trust levels are wired.

| Field | Default | Notes |
| --- | --- | --- |
| Newcomer trust window (days) | 7 | Counted from registration. `0` disables newcomer limits |
| Newcomer topic cooldown (seconds) | 300 | `0` means no extra limit |
| Newcomer comment cooldown (seconds) | 60 | `0` means no extra limit |
| Newcomer daily topic limit | 3 | `0` means no extra limit |
| Newcomer daily comment limit | 30 | `0` means no extra limit |
| Block outbound links for newcomers | on | Topics/comments with http(s) links are rejected during the window |
| Block attachments for newcomers | off | **No effect today**, see the note below |

"`0` means no extra limit" lifts **that one** limit only; global forum cooldowns
and caps still apply.

> **Known issue: "Block attachments for newcomers" has no effect today.** The value
> is stored and displayed, but the posting and upload paths never read it — toggling
> it changes nothing. Use the attachment upload policy itself if you need to block
> uploads. This is recorded in the module note; it is not a failed save.

**Three kinds of account skip newcomer limits** (by design, not a bug):

- users holding `settings.manage` or `topic.edit_any` — otherwise moderators and
  admins would be locked out of their own site;
- bot accounts (AI replies and similar) — they have no login credentials and are
  constrained by AI quotas and review gates instead;
- everyone, when the trust window is `0`.

## Adjusting tier thresholds

Path: **Admin → Forum → Trust levels → the "Thresholds" tab**. One column per tier × six
metrics (days visited, topics, replies, likes received, likes given, bookmarks); `0` means
that metric does not participate in that tier. The number of columns follows the tiers
defined under Tiers. TL0 is read-only here; the page states: "TL0 is governed by the
Newcomers tab and is read-only here."

**Saving takes effect immediately: both tiers and limits are recomputed.** Tier
evaluation and posting limits are one chain, no longer split in two — raising a
threshold demotes users below it, lowering it promotes users who now qualify.

**Earned capability unlocking lives on the same page and is off by default.** See
the next section — the list is empty by default, so **ticking** changes nobody's
capability on a default install. Keep them apart: tiers still change the two TL0 baseline
keys through the baseline gate (previous section), and that has nothing to do with ticking.

## Turning on earned capabilities

Path: **Admin → Forum → Trust levels → the "Capability unlocks" tab** (the same
page as the thresholds), where you tick unlockable permission keys per tier.

**Empty by default is the normal state.** On a default install every tier's earnable
list is empty; the only default capability effect of tiers is the baseline gate from the
previous section (TL0 loses two keys). The default `member` role already holds all 7
unlockable keys, so **turning the list on without touching roles** changes nobody's
capability **through ticking** — to see a ticking effect you must also remove that key
from the role (that is role management, not this page). The unlockable range and the
"can never be unlocked" boundary are in the previous section.

Three things you must know:

**1. Ticking is cumulative.** Ticking a key for one tier unlocks it for that tier
**and every tier above it**. Ticking "create topics" for the second tier gives it to the
third tier and above as well (the example uses default numbering; the numbers shift as
tiers are added or retired). Read the list as "what this tier **adds**", not "what this
tier has"; to give something to the top tier only, tick it there only.

**2. TL0 does not participate in earnable.** TL0 behaviour comes from the seven
newcomer-limit keys; the unlock list has no effect on TL0. TL0 is still affected by the
baseline gate (it lacks two keys) — the two are not the same thing.

**3. All-zero thresholds mean "everyone qualifies".** A tier is reached only when
**all** its metrics are met, and `0` means that metric does not participate. Setting
every metric of a tier to `0` means **every user satisfies that tier** — zero-activity
accounts land in it directly and receive every capability that tier has accumulated.
That is a structural consequence, not a bug: `0` means "not evaluated", not "this tier
is disabled". (The account-age gate still applies: accounts inside the newcomer trust
window reach at most TL1.)

Saving takes effect immediately. Unticking a key removes that earned capability at the
next request load; the tier itself does not drop because of it.

## Restoring defaults

**Restore recommended defaults replaces the whole tier configuration — it is not a
threshold-only reset.** Clicking **"Restore recommended defaults"** asks the server to
replace the entire tier configuration with the recommended default: the number and order
of tiers, every tier's thresholds, per-tier limits, the capabilities each tier adds, tier
names and icons — all of it, and **retired tiers are removed**. A permanent line on the
page states: "Restoring defaults asks the server to replace the whole tier configuration
(tiers, thresholds, limits, earnable unlocks, names and icons) with the recommended
default. Users are re-evaluated against it; earned badges and published content are
unaffected."

This is a **destructive action**: a **blocking confirmation** appears first and lists,
item by item, what will be lost (the same five categories); once confirmed it takes effect
immediately and every user is re-evaluated against the new ladder. To change a single
thing, edit it in its own tab and save — do not use restore defaults.

**Newcomer limits:** click **"Restore newcomer defaults"**; after confirmation the
form is filled with the recommended values and you still have to click **Save** —
restoring only edits the form. The recommended values are the defaults above.

## FAQ

**The admin user detail does not show permissions granted by trust level — is that a
bug?** No. The user detail shows **what was granted** — roles and user-level
overrides. It deliberately does **not** include capabilities earned by trust level.
"What can this account do right now" can only be answered by the authorization check
on an actual request. Do not use an admin screenshot as evidence that "he had no
permission, so how did he post".

**Known gap, stated plainly**: the user detail does not reflect the baseline deduction
either, so a TL0 user's detail page still shows that they can create topics while the
actual request is denied. Trust the request, not the detail page.

**Why is a moderator not limited as a newcomer?** See the three exempt kinds above:
accounts holding `settings.manage` or `topic.edit_any` are excluded.

**What happens if I set the trust window to 0?** Newcomer limits are disabled
entirely; everyone posts under site-default cooldowns and caps. No stored value is
deleted.

**A newcomer's post was rejected for links.** "Block outbound links for newcomers"
is on and the user is still inside the trust window. Either wait for the window to
end or turn that key off.

**Will trust levels invalidate my existing role permissions?** Mostly no, with one
exception. They only **add** capability, never remove it; the one subtraction is the TL0
baseline gate (create topics, upload attachments), restored from TL1 on — and retiring a
tier withdraws the unlockable capabilities that tier contributed. Role grants and
user-level deny overrides keep their current semantics.

**Can a high tier unlock "edit other people's posts"?** No. That family can never be
earned and the admin cannot select it; those capabilities carry privilege without
matching audit.

## Outward-facing behavior

Two things worth knowing:

- **The level query endpoint does not return limits.** The public response carries the
  tier and metric progress only, never per-tier cooldowns or daily caps — publishing
  those would hand rate-limit parameters to everyone. Limits appear only on the admin
  read surface.
- **The profile level card shows other people the tier only.** On someone else's
  profile you see their tier, not progress or bottleneck: the bottleneck may sit on
  "likes given" or "bookmarks", and neither count is public. The progress bar appears
  only on your own profile.

## Related

- [Admin control panel](./admin.md) — users, roles, and permissions overall
- [Forum day-to-day](./forum.md) — posting and moderation
- [Account & security](./account-security.md) — registration, login, sessions
