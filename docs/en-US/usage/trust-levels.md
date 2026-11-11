# Trust levels

[← Usage](./README.md)

> **Status: live (verified 2026-11-11).** The five-tier evaluation, the threshold
> policy, the load-time merge, the product flag, the admin threshold tab
> ("Settings → Trust levels"), the public level query endpoint, and the profile level
> card are all implemented and wired: **saving thresholds immediately changes tier
> evaluation and per-tier limits**. Earned capability unlocking is off by default and
> must be configured per tier by the operator — see "Turning on earned capabilities".
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

## The five tiers

| Tier | Name | Role |
| --- | --- | --- |
| TL0 | Newcomer | Just signed up; identical to today's newcomer limits |
| TL1 | Member | Past the trust window |
| TL2 | Resident | Consistent output |
| TL3 | Veteran | Long-term contributor |
| TL4 | Leader | Core of the community |

### Default thresholds

A tier is reached only when **all** its metrics are met; `0` means that metric does
not participate in that tier.

**The actual numbers are whatever Settings → Trust levels shows right now.** That page
has a "Restore recommended defaults" button — click it to see the built-in values.
This page does not copy the numbers: thresholds are configurable, a copy here will
eventually diverge from what the site uses, and nobody notices the divergence.

TL0 does not participate in evaluation; its limits come from the seven newcomer
keys (next section).

**Account-age gate**: inside the "newcomer trust window" a user can reach at most
TL1 — enough activity leaves TL0 early, but never skips past TL1. That window is the
same value as the newcomer trust window.

**About limits**: site-default posting cooldowns and daily caps are `0` (no extra
restriction), so there is **no observable limit difference** between TL1 and TL4
today. "Higher tiers skip cooldowns" is not true in this codebase and is not
written here.

## Two keys are level-gated; nothing else unlocks by default

Keep two different things apart:

- **Earned capability unlocking (earnable) is an empty set by default**: every tier's
  unlock list is empty and must be filled in per tier by the operator. That has not changed.
- **System baseline gating**: `topic.create` (create topics) and `attachment.upload`
  (upload attachments) are **not decided by earnable** — a code-level baseline constant
  decides them. On a default install **TL0 does not have these two keys and TL1 and above
  do**, and the admin cannot turn them off.

So on a default install tiers affect **three things**: **tiered limits, tier display, and
whether these 2 baseline keys are available**.

The unlockable set is a **positive whitelist of exactly 7 content-creation
permissions**: create topics, reply, upload attachments, edit your own topic, edit
your own reply, delete your own topic, delete your own reply.

**These can never be unlocked** (the admin cannot even select them): editing or
deleting **other people's** content (`*_any`), pinning, locking, banning users,
moderation, management permissions, settings permissions, extension permissions,
admin access. The reason is not conservatism: those capabilities carry privilege
without matching audit, so handing them out via "earn enough likes" would create
actions nobody can trace.

### The two baseline-gated keys (on by default)

| Key | What it does | TL0 | TL1 and above |
| --- | --- | --- | --- |
| `topic.create` | Create new topics | no | yes |
| `attachment.upload` | Upload attachments (**avatar uploads use this key too**) | no | yes |

- **Why TL0 lacks them**: these are things "full members" do; TL0 keeps replying and
  editing/deleting its own content.
- **How to get them back**: reach the TL1 threshold (numbers are whatever the trust level
  page shows right now), or pass the newcomer trust window — once it expires a user is at
  least TL1.
- **Ticking in the admin cannot change them**: they are system constants, not part of the
  earnable list; the page shows them as level-gated with no checkbox.
- **Nothing is withheld while the flag is off**: turning `features.trust_levels` off
  disables the baseline gate entirely, so all seven keys are present, exactly as before.
- **Avatars**: changing an avatar and uploading an attachment share one key, so a TL0 user
  cannot change their avatar either; it comes back from TL1.

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

Path: **Admin → Settings → the "Trust levels" tab** (title "Trust level
thresholds"). Five tiers × six metrics (days visited, topics, replies, likes
received, likes given, bookmarks); `0` means that metric does not participate in
that tier.

**Saving takes effect immediately: both tiers and limits are recomputed.** Tier
evaluation and posting limits are one chain, no longer split in two — raising a
threshold demotes users below it, lowering it promotes users who now qualify.

**Earned capability unlocking lives on the same page and is off by default.** See
the next section — the list is empty by default, so **ticking** changes nobody's
capability on a default install. Keep them apart: tiers still change the two TL0 baseline
keys through the baseline gate (previous section), and that has nothing to do with ticking.

## Turning on earned capabilities

Path: **Admin → Settings → the "Trust levels" tab** (the same page as the
thresholds), where you tick unlockable permission keys per tier.

**Empty by default is the normal state.** On a default install every tier's earnable
list is empty; the only default capability effect of tiers is the baseline gate from the
previous section (TL0 loses two keys). The default `member` role already holds all 7
unlockable keys, so **turning the list on without touching roles** changes nobody's
capability **through ticking** — to see a ticking effect you must also remove that key
from the role (that is role management, not this page). The unlockable range and the
"can never be unlocked" boundary are in the previous section.

Three things you must know:

**1. Ticking is cumulative.** Ticking a key for one tier unlocks it for that tier
**and every tier above it**. Ticking "create topics" for TL2 gives it to TL3 and TL4
as well. Read the list as "what this tier **adds**", not "what this tier has"; to give
something to the top tier only, tick it there only.

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

**Tier thresholds (usable today):** click **"Restore recommended defaults"** to fill
the five tiers back to the values in the table above. A permanent line on the page
states: "Restoring defaults only resets the five tier thresholds and does not affect
tiers users have already earned." You still have to click Save.

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
baseline gate (create topics, upload attachments), restored from TL1 on. Role grants and
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
