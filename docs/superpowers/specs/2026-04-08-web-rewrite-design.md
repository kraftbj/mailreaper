# MailReaper Web — Design Spec

## Overview

Rewrite MailReaper from a Thunderbird extension into a standalone Go application with a web dashboard. The core mission shifts from "expire time-sensitive emails" to "triage all incoming mail" — routing messages to user-defined destination folders based on prioritized rules (pattern matching, RFC headers, regex, LLM analysis). The inbox becomes a staging area; the goal is zero messages remaining.

## Architecture

### Single Go Binary

One statically-compiled binary containing:

- **Scanner** — cron-scheduled IMAP fetch and evaluation loop
- **Rule Engine** — evaluates messages against prioritized rules, returns triage verdicts
- **Action Executor** — executes verdicts via IMAP (move, flag, delete) with a confidence gate
- **Web Server** — serves the dashboard UI and exposes a JSON API for it
- **Feedback Detector** — checks for messages moved back to Inbox as implicit corrections

### Deployment

Docker container (~15MB image from Go static binary). Single `docker-compose.yml` for local development. Mount a volume for SQLite database and config file. Runs locally on macOS during development, eventually on a home server or NAS.

### Configuration

YAML config file for sensitive/environment-specific values:

```yaml
accounts:
  - name: personal
    host: imap.fastmail.com
    port: 993
    username: user@fastmail.com
    password: ${IMAP_PASSWORD}  # env var substitution
    tls: true
    folders:
      scan: [INBOX]

llm:
  provider: gemini  # gemini | ollama | none
  gemini:
    api_key: ${GEMINI_API_KEY}
    model: gemini-2.5-flash
  ollama:
    endpoint: http://localhost:11434
    model: qwen2.5:7b

scan:
  interval_minutes: 30
  min_message_age_minutes: 60
  max_messages_per_scan: 100

server:
  port: 8025
  # No auth in v1 — intended for local/LAN use behind a reverse proxy
```

Rules, activity log, verdicts, training examples, and LLM cache live in SQLite (editable via dashboard).

## Data Model (SQLite)

### `accounts`
Stores IMAP account metadata synced from config. Separates config-sourced connection info from runtime state.

| Column | Type | Notes |
|--------|------|-------|
| id | TEXT PK | slug from config name |
| name | TEXT | display name |
| last_scan_at | TIMESTAMP | |
| last_scan_message_count | INT | |

### `rules`
Triage rules, ordered by priority.

| Column | Type | Notes |
|--------|------|-------|
| id | TEXT PK | `builtin-*` or `user-*` |
| name | TEXT | |
| enabled | BOOL | |
| priority | INT | lower = higher priority |
| builtin | BOOL | |
| match_config | JSON | `{senderPatterns, subjectPatterns, folders, headerMatch}` |
| expiration_config | JSON | `{type, hours, pattern, prompt, category}` |
| action | TEXT | move, delete, tag |
| destination_folder | TEXT | target folder name (e.g. "Paper-Trail", "Newsletters") |
| grace_period_days | INT | |
| created_at | TIMESTAMP | |
| updated_at | TIMESTAMP | |

### `verdicts`
Every evaluation result, including queued low-confidence verdicts awaiting review.

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER PK | |
| account_id | TEXT FK | |
| message_id_header | TEXT | RFC Message-ID, unique constraint |
| subject | TEXT | |
| sender | TEXT | |
| sent_at | TIMESTAMP | |
| rule_id | TEXT FK | |
| status | TEXT | `pending`, `executed`, `approved`, `rejected`, `corrected` |
| destination_folder | TEXT | where it was/would be moved |
| expires_at | TIMESTAMP | nullable |
| reason | TEXT | |
| confidence | REAL | |
| evaluated_at | TIMESTAMP | |
| acted_at | TIMESTAMP | nullable |

**Status lifecycle:**
- High confidence → `executed` immediately
- Low confidence → `pending` → user approves (`approved`) or rejects (`rejected`)
- User moves message back to Inbox → `corrected`

### `activity_log`
Denormalized audit trail for the dashboard feed.

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER PK | |
| type | TEXT | moved, classified, deleted, tagged, corrected, error, grace_deleted |
| account_id | TEXT | |
| message_id_header | TEXT | |
| subject | TEXT | |
| sender | TEXT | |
| rule_name | TEXT | |
| destination | TEXT | |
| reason | TEXT | |
| confidence | REAL | |
| created_at | TIMESTAMP | |

### `llm_cache`
Cached LLM verdicts with tiered TTL (same as current: 10m errors, 24h positive, 7d negative).

| Column | Type | Notes |
|--------|------|-------|
| message_id_header | TEXT PK | |
| verdict | JSON | |
| cached_at | TIMESTAMP | |

### `training_examples`
Corrections and confirmations used for few-shot LLM prompting.

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER PK | |
| category | TEXT | expiry, receipt, newsletter, etc. |
| subject | TEXT | |
| sender | TEXT | |
| body_snippet | TEXT | |
| label | TEXT | correct destination or "keep" |
| source | TEXT | correction, dashboard, auto |
| created_at | TIMESTAMP | |

### `categories`
User-defined triage destinations. Links a folder name to display metadata.

| Column | Type | Notes |
|--------|------|-------|
| id | TEXT PK | slug |
| name | TEXT | display name |
| folder_name | TEXT | IMAP folder to create/use |
| icon | TEXT | emoji or symbol |
| color | TEXT | hex color for dashboard |
| created_at | TIMESTAMP | |

## Mail Access (IMAP)

Use `go-imap` (v2) for all mail operations.

### Scan Cycle

1. Connect to each configured account
2. `SELECT` each scan folder (default: INBOX)
3. `SEARCH` for messages newer than last scan timestamp and older than `min_message_age_minutes`
4. `FETCH` envelope (sender, subject, date) + headers for matched messages
5. Evaluate against rules (body fetched lazily only when needed by content-regex or LLM rules)
6. Execute verdicts:
   - High confidence → IMAP `MOVE` to destination folder (create if needed), status = `executed`
   - Low confidence → store as `pending` verdict, no IMAP action
7. Update `last_scan_at`

### Feedback Detection

After the main scan, for each account:

1. Check the Inbox for messages whose Message-ID exists in the `verdicts` table with status `executed`
2. If found → message was moved back to Inbox by the user
3. Mark verdict as `corrected`
4. Auto-create a training example: `{category, subject, sender, label: "keep", source: "correction"}`
5. Invalidate LLM cache for that Message-ID

### Grace Period Cleanup

Separate scheduled task (every 6 hours):

1. For each destination folder with grace period rules, list messages
2. Check `verdicts` table for `acted_at` timestamp
3. If `now - acted_at > grace_period_days` → permanent delete via IMAP
4. Log as `grace_deleted`

### Rule Suggestion

When the system detects repeated corrections or repeated manual classifications of similar messages:

1. After N corrections from the same sender pattern (e.g. 3), propose a new rule
2. Surface the suggestion in the dashboard: "I noticed you keep moving emails from *@delwood.org* to Delwood. Create a rule?"
3. User can accept (creates the rule), dismiss, or modify

## Rule Engine

Ported from the existing JS implementation. Same rule types, same priority-based evaluation, same first-match-wins semantics.

### Rule Types

| Type | Behavior |
|------|----------|
| `ttl` | Expire N hours after send date |
| `header` | Parse RFC `Expires` header |
| `content-regex` | Regex with capture group for date extraction |
| `llm` | LLM expiration analysis with confidence score |
| `classify` | Pattern-based classification → destination folder |
| `llm-classify` | LLM-based classification → destination folder |

### Confidence Gate

Each rule type produces a confidence score (0.0–1.0). Deterministic rules (TTL, header, regex, pattern classify) always produce 1.0. LLM rules produce variable confidence.

- `confidence >= threshold` → auto-execute (threshold configurable, default 0.7)
- `confidence < threshold` → queue as pending verdict for dashboard review

Pending verdicts that are approved or rejected become training examples automatically.

## Web Dashboard

### Tech Stack

- Go `net/http` for the API server
- Vanilla HTML/JS/CSS, embedded in the binary via `embed.FS`
- No frontend framework — simple enough to not need one
- Server-sent events (SSE) for live dashboard updates during scans

### Pages

**Dashboard** (landing page):
- Inbox health: remaining count, triage percentage, messages scanned today
- Triage destination cards: each category with today's count, click to drill into messages
- Recent activity feed with color-coded destination labels
- Review queue panel for pending low-confidence verdicts (accept/change/skip)
- Correction indicator when messages are moved back to Inbox

**Review Queue** (full page):
- All pending verdicts, sortable by confidence, date, suggested destination
- Batch operations (approve all above X confidence)
- "Change →" to redirect to a different destination folder
- Each decision feeds back as a training example

**Rules**:
- List all rules sorted by priority, drag to reorder
- Create/edit rules with the same field structure as current extension
- Test a rule against recent messages (dry run)
- Rule suggestions from the feedback detector

**Settings**:
- Account status (connected, last scan, error state)
- LLM provider config with connection test
- Scan schedule and thresholds
- Category/folder management (add, rename, set icon/color)
- Training examples browser (view, delete)

## Chained Actions (Design for V1, Implement in V2)

Messages can flow through a pipeline of destinations:

```
Inbox → [triage folder] → [grace period] → [final destination] → [grace period] → delete
```

Examples:
- `Inbox → Orders → (72h after delivery) → Paper-Trail → (90d) → delete`
- `Inbox → Expired → (7d) → delete`
- `Inbox → Newsletters → (14d) → delete`
- `Inbox → Delwood → (keep forever)`

**Data model support:** The `rules` table includes a `next_rule_id` column (nullable FK to another rule) that triggers after the grace period expires. When the grace period cleanup runs, instead of deleting, it evaluates the next rule in the chain. If `next_rule_id` is null, the message is permanently deleted (current behavior).

**V1 implementation:** The `next_rule_id` column exists in the schema but the grace period cleanup always deletes (ignores chains). The dashboard shows the chain configuration UI as read-only / coming soon. This keeps the schema stable for V2 without adding runtime complexity now.

## What's NOT in V1

- Multi-user / authentication (local/LAN use assumed)
- IMAP IDLE / push notifications (polling only)
- Email content preview in dashboard (just metadata)
- Mobile-optimized UI
- Chained action execution (schema ready, runtime deferred)
- SMTP / sending capability
- Encryption at rest for the database
