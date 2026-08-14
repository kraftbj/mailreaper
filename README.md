# MailReaper

AI-powered email triage and expiration manager. Automatically classifies, files, and expires time-sensitive emails so your inbox only contains things that need your attention.

MailReaper exists in two forms: a **Thunderbird extension** (Manifest V3) and a **standalone Go server** that connects via IMAP. The `web-rewrite` branch is the Go server, which is the active development target.

## What It Does

MailReaper scans your inbox on a schedule and evaluates each message against a prioritized list of rules:

- **Static rules** (fast, free): Pattern-match on sender/subject and classify or expire. "Any email from `@chase.com` goes to Paper-Trail."
- **AI classify rules** (LLM-powered): A single multi-category LLM call classifies messages into triage folders (receipts, newsletters, notifications, promotions, hobbies) and detects expiry dates simultaneously.
- **Header rules** (standards-based): Honor the RFC `Expires` header when senders set it.
- **Auto-generated rules**: The system learns from your manual classifications. After you drag 3+ messages from the same sender to the same folder, a static rule is created automatically.

Messages are moved to triage folders under `Folders/AI-Triage/`:
- **Expired** -- time-sensitive emails past their date
- **Newsletters** -- digests, publications, editorial content
- **Notifications** -- automated alerts, status updates, tracking
- **Paper-Trail** -- receipts, statements, payment confirmations
- **Promotions** -- marketing, sales, re-engagement emails
- **Hobbies** -- gaming, tabletop RPG, crowdfunding updates

Notifications auto-expire after 3 days and promotions after 7 days. Messages with detected future expiry dates (e.g. "event on Saturday") are classified now and automatically moved to Expired when the date passes.

## Go Server (`web/`)

### Setup

```bash
cd web
cp config.yaml.example config.yaml  # edit with your IMAP and LLM settings
go build -o mailreaper ./cmd/mailreaper
./mailreaper
```

The web dashboard runs at `http://localhost:8025`.

### Configuration

Edit `config.yaml`:

```yaml
accounts:
  - name: Personal
    host: imap.example.com
    port: 993
    username: you@example.com
    password: ${IMAP_PASSWORD}  # env var substitution supported
    tls: true
    folders:
      scan:
        - INBOX

llm:
  provider: gemini  # gemini | ollama | none
  gemini:
    api_key: ${GEMINI_API_KEY}
    model: gemini-3.5-flash-lite
    service_tier: flex  # 50% cost reduction, uses spare capacity
  ollama:
    endpoint: http://localhost:11434
    model: qwen2.5:7b

scan:
  interval_minutes: 30
  min_message_age_min: 60
  max_messages_per_scan: 100

server:
  port: 8025
  bind_addr: 127.0.0.1  # loopback only by default; the dashboard has no auth
  # allowed_hosts:      # only needed if bind_addr is not loopback (see below)
  #   - mail.example.internal

# IANA timezone for interpreting LLM-extracted deadlines that carry no UTC
# offset (defaults to the host zone, which is UTC inside a container).
timezone: America/Chicago
```

### LLM Providers

**Gemini (recommended):**
- Get an API key at [Google AI Studio](https://aistudio.google.com/apikey)
- The `flex` service tier halves the cost by using spare capacity (with automatic retry and fallback to standard tier on 503s)
- Cost at typical volumes: under $0.15/month

**Ollama (local):**
- Install [Ollama](https://ollama.com/) and pull a model: `ollama pull qwen2.5:7b`
- Free but slower and less accurate than Gemini, especially for date reasoning

### Flags

- `--config path` -- config file (default: `config.yaml`)
- `--db path` -- SQLite database (default: `mailreaper.db`)
- `--catchup` -- scan all messages on first run, not just the last 7 days

### API

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/rules` | List all rules |
| POST | `/api/rules` | Create/update a rule |
| DELETE | `/api/rules/{id}` | Delete a rule |
| GET | `/api/activity?limit=N` | Recent activity log |
| GET | `/api/verdicts/pending` | Pending verdicts |
| PUT | `/api/verdicts/{id}/status` | Approve/reject a verdict |
| GET | `/api/categories` | List triage categories |
| POST | `/api/categories` | Create/update a category |
| GET | `/api/stats` | Dashboard statistics |
| POST | `/api/scan` | Trigger a scan |
| POST | `/api/rescan` | Clear all verdicts and full rescan |
| POST | `/api/refresh` | Clear pending verdicts and LLM cache, then scan -- the cheap workaround for the LLM cache's message-ID-only key limitation, without the full data loss of `/api/rescan` |

There is no authentication on this API. To close cross-origin CSRF exposure,
non-GET requests must send `Content-Type: application/json` and, when the
browser sets it, a same-origin (or absent) `Sec-Fetch-Site` header. The
bundled dashboard already does this. A curl invocation or script that POSTs
or DELETEs without setting `Content-Type: application/json` will now get a
403 -- add that header to any external client. This is a mitigation against
browser-driven attacks, not authentication; anything that can reach
`localhost:8025` directly can still call the API.

Every request, including `GET`, is also checked against a Host allowlist
(`localhost`, `127.0.0.1`, `[::1]`, `::1` by default) to close a DNS-rebinding
gap the `Sec-Fetch-Site`/`Content-Type` checks above cannot: an attacker page
served from a hostname that resolves to your loopback address can make the
browser send a legitimate same-origin, JSON-typed request. Set
`server.allowed_hosts` in `config.yaml` if you run this behind a reverse
proxy with a real hostname -- otherwise every request is rejected once
`bind_addr` is not loopback.

## How It Learns

1. **Manual classification**: Drag a message to a triage folder. The next scan detects it, records a training example, and the LLM uses it as a few-shot reference.

2. **Rule distillation**: After 3+ messages from the same sender are manually classified to the same folder, a static `classify` rule is auto-created. Static rules are faster (no LLM call) and 100% reliable.

3. **Correction feedback**: If you move a message back to the inbox after MailReaper triaged it, the system records the correction and updates its training examples.

4. **SimpleLogin support**: Messages forwarded through SimpleLogin aliases are matched against rules using the `X-SimpleLogin-Original-From` header, so rules like `*@capmetro.org` work even when the envelope sender is `*@simplelogin.co`.

## Architecture (Go Server)

```
web/
├── cmd/mailreaper/main.go     # Entry point, scan loop, signal handling
├── internal/
│   ├── config/                # YAML config with env var substitution
│   ├── db/                    # SQLite (modernc.org/sqlite, WAL mode)
│   │   ├── verdicts.go        # Per-message expiration decisions
│   │   ├── rules.go           # Rule CRUD and seeding
│   │   ├── activity.go        # Activity log
│   │   ├── llmcache.go        # LLM response cache with tiered TTL
│   │   ├── training.go        # Few-shot examples (50 per category)
│   │   ├── categories.go      # Triage folder definitions
│   │   └── distill.go         # Pattern mining for auto-rule creation
│   ├── imap/                  # go-imap/v2 client wrapper
│   ├── llm/
│   │   ├── adapter.go         # Gemini/Ollama with retry and flex fallback
│   │   └── prompts.go         # Analysis, classification, multi-classify
│   ├── rules/
│   │   ├── engine.go          # Rule matching and evaluation
│   │   ├── defaults.go        # Built-in rule definitions
│   │   └── glob.go            # Glob pattern matching
│   ├── scanner/
│   │   ├── scanner.go         # Main scan loop, multi-classify batching
│   │   ├── feedback.go        # Correction detection, manual classification
│   │   ├── distill.go         # Training-to-rule promotion
│   │   └── sweep.go           # Deferred expiry and folder age-out
│   └── server/                # HTTP API and SSE
└── scripts/
    └── seed-user-rules.sh     # Bulk-add sender-specific rules via API
```

### Scan Cycle

1. Connect to IMAP, fetch messages from configured folders
2. For each message, evaluate against enabled rules sorted by priority:
   - Static rules (TTL, header, content-regex, classify) run first
   - LLM rules batch all classify categories into a single call that also detects expiry
3. High-confidence verdicts (>= 0.7) auto-execute; low-confidence queue as pending
4. Detect manual classifications in triage folders, record training examples
5. Sweep deferred expiries (future dates that have now passed)
6. Sweep aged-out notifications (3 days) and promotions (7 days)
7. Distill manual classification patterns into static rules

## Thunderbird Extension

The original Thunderbird extension is in the root directory. See `manifest.json` for the extension manifest. Load it via Tools > Add-ons > Debug Add-ons > Load Temporary Add-on.

## License

MIT
