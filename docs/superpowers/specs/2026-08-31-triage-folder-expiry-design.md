# Expiring mail that already lives in a triage folder

Date: 2026-08-31
Status: approved, not yet implemented

## Problem

A promotional email is triaged into Hobbies or Promotions and stays there
forever, even when its own body says "sale ends this Friday". On Saturday it
should be in Expired. It isn't.

Two independent defects produce this.

### Defect 1: `classify` rules bypass deadline extraction by construction

`evaluateMessage` (`internal/scanner/scanner.go`) is first-match-wins over
rules sorted by priority. `rules.EvaluateClassify` returns a verdict with no
body fetch, no LLM call, and no `ExpiresAt`:

```go
func EvaluateClassify(rule db.Rule) *RuleVerdict {
	return &RuleVerdict{Classified: true, Rule: rule, Reason: rule.Name, Confidence: 1.0}
}
```

The live database has 77 enabled `classify` rules at priority 27-30, mostly
`auto-*` rules distilled from user corrections (`emails@emails.cinemark.com →
Promotions`, `no-reply@dmsguild.com → Hobbies`). They short-circuit the `llm`
rule at priority 100 and the `llm-classify` rules at 200-203, which are the
only rules that ever ask for a deadline.

Executed verdicts, grouped by the type of the rule that produced them:

| rule type | verdicts | carrying an `expires_at` |
|---|---|---|
| `classify` | 4,359 | 0 |
| `llm-classify` | 3,483 | 205 |

Zero is not a sampling artifact. It is structural: that code path cannot
produce a deadline.

Manually-filed messages (`status='manual'`, ~1,480 rows) have the same
problem for the same reason — `DetectManualClassifications` records where the
user put a message, never whether it expires.

### Defect 2: `SweepDeferredExpiries` does not clear its backlog

Even the messages that *do* carry a deadline are not moving. 191 verdicts have
an `expires_at` in the past and are still recorded in a triage folder:

| folder | rows | oldest deadline |
|---|---|---|
| Notifications | 94 | 2026-04-30 |
| Promotions | 87 | 2026-04-29 |
| Hobbies | 8 | 2026-05-02 |
| Newsletters | 2 | 2026-07-05 |

Four months stuck. Two causes, both in `internal/scanner/sweep.go`:

1. `client.GetMessagesInFolder(v.DestinationFolder)` is called **inside the
   per-verdict loop**. Each call is a `SELECT` + `SEARCH ALL` + `FETCH
   ENVELOPE` over a folder holding thousands of messages. With 191 stuck rows
   that is 191 full folder listings per scan cycle, and the same folder is
   re-fetched dozens of times in a single sweep.
2. When the message is not found in its recorded folder — the user moved it,
   or deleted it — the loop `break`s and the verdict is left untouched. It is
   past-due forever, re-listed and re-fetched on every subsequent sweep. The
   backlog is therefore self-sustaining: it can only grow.

Fixing defect 1 without fixing defect 2 would feed newly-extracted deadlines
into a sweep that already cannot drain.

## Goals

- A message routed into a perishable triage folder by any rule gets its
  content-implied deadline extracted, and moves to Expired when that deadline
  passes.
- The sweep actually drains, in bounded IMAP work per cycle.
- Messages already sitting in those folders are caught up.

## Non-goals

- Per-category maximum age ("anything in Promotions older than 60 days is
  expired"). Discussed and deliberately deferred to a separate piece of work.
- Changing the prompts, the model, or the confidence threshold.
- Reworking rule priority or the first-match-wins contract itself. Deadline
  extraction stops being something a routing rule can short-circuit; routing
  stays exactly as it is.

## Design

### 1. Categories gain a `check_deadlines` flag

Not every category is perishable. Paper-Trail receipts and Newsletters do not
expire; Promotions, Notifications, and Hobbies do. Newsletters alone accounts
for 2,198 of the 4,359 `classify`-routed verdicts, so gating on the category
is also the single largest cost reduction available.

`categories` gains a column:

```sql
check_deadlines INTEGER NOT NULL DEFAULT 0
```

Added to the base `CREATE TABLE` in `internal/db/db.go`, plus a one-shot
migration `v7_category_check_deadlines` that follows the established
probe-before-acting pattern of `migrateLLMCacheCompositeKey` and
`migratePlacements`: read `PRAGMA table_info(categories)`, return early if the
column is already present (fresh database), otherwise `ALTER TABLE ... ADD
COLUMN` and seed it.

Seed values: `1` for `promotion`, `notification`, `hobbies`; `0` for
`receipt`, `newsletter`, `expired`.

The default is `0`. A category the user adds later, and never thinks about,
must not start silently expiring mail — false negatives over false positives
is the standing bias of this codebase, and it applies here.

`db.Category` gains a `CheckDeadlines bool` field serialized as
`checkDeadlines`, carried through `GetCategories`, `SaveCategory`, and
`handleSaveCategory`. The
existing category modal in `ui/settings.html` gains a checkbox
(`cat-check-deadlines`) wired through `openCategoryModal` and `saveCategory`
in `ui/js/settings.js`; `renderCategories` gains a column showing it.

Putting the flag on the category rather than on rules means the 77 auto-
distilled `classify` rules need no changes at all — each already names a
destination folder, and the folder maps to a category.

### 2. Extract a deadline after routing, when the route did not supply one

In `ScanAccount`, after `evaluateMessage` returns a non-nil verdict and before
the verdict is persisted:

```
verdict.ExpiresAt != nil                → unchanged (today's behavior)
destination folder matches no category's
  folder_name, or matches one whose
  check_deadlines = 0                   → unchanged
otherwise                               → one BuildAnalysisPrompt call,
                                          cached under the existing
                                          "analysis" prompt kind

    past deadline    → destination overridden to the canonical Expired
                       folder; activity type "expired"
    future deadline  → routing rule's folder kept, expires_at persisted;
                       SweepDeferredExpiries acts on it on the day
    no deadline      → unchanged
```

The folder-to-category lookup compares the verdict's destination folder against
each category's `folder_name` with `strings.EqualFold`, matching how
`backfill.go` already compares destinations.

Extraction runs only on the auto-execute branch (`confidence >= 0.7`), where
the message is actually moved into the folder. A low-confidence verdict is
queued as `pending` and the message stays in the inbox awaiting review, so
there is nothing sitting in a triage folder to expire and no reason to spend a
call on it.

This is placed after routing rather than before it so that `llm-classify`
messages, which already receive a deadline from their combined
classify+extract call, pay nothing extra. Only pattern-routed messages into a
perishable category add a call — historically ~1,815 of the 4,359, versus
~4,400 if extraction ran unconditionally ahead of routing.

The implementation reuses `extractValidExpiresAt` unchanged: the
`minExpiryConfidence` gate, the `sentAt` plausibility floor, `maxExpiryHorizon`,
and `endOfDay` date-only handling all apply exactly as they do on the
`llm-classify` path. No new date parsing is introduced.

The extraction result is written to the `llm_cache` under the existing
`analysis` cache key, so a message costs at most one call, not one per scan.

New code lives in `internal/scanner/deadline.go` rather than growing
`scanner.go`, which is already 862 lines.

### 3. `SweepDeferredExpiries` is rewritten to be bounded and self-draining

`internal/scanner/sweep.go`:

- **Group expiries by destination folder before doing any IMAP work.** Fetch
  each folder once, build a `map[messageID]uid`, then resolve every verdict
  for that folder from the map. IMAP round trips drop from O(verdicts) to
  O(distinct folders) — from 191 to at most 5 in the current backlog.
- **Terminate the unfindable.** When a verdict's message is not in the map,
  the message has left that folder and the verdict can never be satisfied.
  Set its status to `orphaned` and log it. `GetDeferredExpiries` filters
  `status IN ('executed','manual')`, so an orphaned row drops out and stops
  being re-examined every cycle. `expires_at` is preserved for forensics.
  `orphaned` is a new status; it is not referenced by
  `DeleteVerdictsByStatus`, the pending-verdict query, or the dashboard, and
  `ui/js/app.js` falls through to its default badge, so the blast radius is
  contained.
- **Scope the query to the account.** `GetDeferredExpiries` takes no
  `account_id` today, so in a multi-account configuration every account's
  expiries are swept against whichever account's IMAP client is connected —
  guaranteeing not-found for the other account's messages, and with the change
  above, incorrectly orphaning them. Add the filter as part of this work;
  `SweepDeferredExpiries` already receives `accountID`.

### 4. Backfill catches up what is in the folders now

`BackfillFolders` (`internal/scanner/backfill.go`) already walks the live IMAP
folders via `GetMessagesInFolder`, so it naturally covers only messages
actually present today rather than the historical verdict count — which is
what we want, since much of that history has since been deleted or archived.

Extend it so that when re-evaluation produces a verdict with no deadline and
the folder is a `check_deadlines` category, it performs the same extraction
step 2 performs. This is also what sweeps up manually-filed messages: they are
physically in the folder even though their verdicts were never given a
deadline.

Backfill already calls `RemoveCachedVerdict` before re-evaluating, so the
extraction is a genuine re-ask rather than a cache read.

## Data flow

```
message in INBOX
  │
  ├─ evaluateMessage → first matching rule wins → destination folder
  │
  ├─ verdict has ExpiresAt?  ── yes ──────────────────────────┐
  │        │ no                                               │
  │        ▼                                                  │
  ├─ destination is a check_deadlines category? ── no ──┐      │
  │        │ yes                                        │      │
  │        ▼                                            │      │
  │   BuildAnalysisPrompt (cached, one call ever)       │      │
  │        │                                            │      │
  │        ├─ past deadline   → destination = Expired   │      │
  │        ├─ future deadline → expires_at persisted ───┼──────┤
  │        └─ no deadline     → unchanged ──────────────┤      │
  │                                                     │      │
  ▼                                                     ▼      ▼
save verdict, move message                        (no deadline recorded)
  │
  │  … later scan cycles …
  ▼
SweepDeferredExpiries
  ├─ group past-due verdicts by folder, one fetch per folder
  ├─ found   → move to Expired, status executed, activity "expired"
  └─ missing → status orphaned, stop retrying
```

## Testing

`internal/scanner` has real coverage against the `MailClient` mock; these
extend it rather than introducing new machinery.

**Deadline extraction (`deadline_test.go`, new):**
- A `classify`-routed message into a `check_deadlines=1` category with a past
  deadline is redirected to Expired, not the rule's folder.
- The same message with a future deadline keeps the rule's folder and persists
  `expires_at`.
- A `classify`-routed message into a `check_deadlines=0` category (Newsletters,
  Paper-Trail) makes no LLM call at all — asserted on the mock's call count,
  because the cost argument is the reason the flag exists.
- A verdict that already carries an `ExpiresAt` makes no second call.
- A message whose destination maps to no category makes no call.
- Extraction below `minExpiryConfidence`, and one implausible against `sentAt`,
  are both dropped and leave the routing untouched.

**Sweep (`sweep_test.go`, extend):**
- Twenty past-due verdicts spread across three folders produce exactly three
  `GetMessagesInFolder` calls. This is the regression that matters — it is the
  defect that stalled the live backlog.
- A verdict whose message is absent from its folder becomes `orphaned` and is
  not returned by the next `GetDeferredExpiries`.
- A verdict for another account is not swept, and specifically is not
  orphaned, by the connected account's client.

**Backfill (`backfill_test.go`, extend):**
- A message in a perishable folder with no prior deadline gets one extracted
  and is moved when it is past.
- A message in a non-perishable folder is left alone with no LLM call.

**Migration (`db_test.go`, extend):**
- A database created before the column gains it with the correct seeds.
- A fresh database is not double-migrated and keeps its defaults.
- Re-running `applyOneShotMigrations` is a no-op.

## Rollout

1. Land the schema and UI change; confirm the flags read correctly in Settings.
2. Land the sweep rewrite alone and run one scan cycle. The 191-row backlog
   should drain or orphan in that single cycle. This is independently
   verifiable before any new extraction spend is incurred.
3. Land the extraction change and watch one live scan.
4. Run `-backfill` against the perishable folders.

Steps 2 and 3 are separable on purpose: if the sweep rewrite alone clears the
backlog, that confirms defect 2's diagnosis before defect 1's fix adds
anything new to sweep.
