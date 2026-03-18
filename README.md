# MailReaper 🗡️📧

AI-powered email expiration manager for Thunderbird. Automatically identifies and cleans up time-sensitive emails that have passed their useful life.

## What It Does

MailReaper scans your configured email folders on a schedule and evaluates each message against a prioritized list of expiration rules:

- **Static rules** (fast, free): Pattern-match on sender/subject with a fixed TTL. "Any email from `@capmetro.org` expires 2 hours after it was sent."
- **AI rules** (LLM-powered): Analyze email body content for time-sensitive deadlines. "This sale ended at midnight last night."
- **Header rules** (standards-based): Honor the emerging `Expires` header RFC when senders set it.

Expired emails are moved to an "Expired" folder, then permanently deleted after a configurable grace period.

## Installation

MailReaper is not yet available on [addons.thunderbird.net](https://addons.thunderbird.net). To install it, build the `.xpi` package and load it manually.

### Building

```bash
make
```

This produces `mailreaper.xpi` in the project root.

### Installing the .xpi

1. Open Thunderbird
2. Go to **Add-ons Manager** (Tools → Add-ons and Themes)
3. Click the gear icon → **Install Add-on From File…**
4. Select `mailreaper.xpi`

### Loading for development

1. Go to **Add-ons Manager** → gear icon → **Debug Add-ons**
2. Click **Load Temporary Add-on**
3. Select `manifest.json` from this directory

## Configuration

Click the MailReaper toolbar button → **Settings**, or go to Add-ons → MailReaper → Preferences.

### Quick Start

1. **Folders tab**: Select which folders to scan (defaults to Inbox if none selected)
2. **Rules tab**: Enable/disable built-in rules, add your own
3. **AI tab**: Optionally configure Gemini or Ollama for content analysis
4. **General tab**: Adjust scan interval and grace period

### LLM Setup

**Gemini (cloud):**
- Get an API key at [Google AI Studio](https://aistudio.google.com/apikey)
- Select a model (2.5 Flash is cheapest, 3 Flash is smartest)
- Cost is essentially zero at typical email volumes (~$0.002/day)

**Ollama (local/self-hosted):**
- Install [Ollama](https://ollama.com/) and pull a model: `ollama pull llama3.2:3b`
- Set the endpoint URL (default: `http://localhost:11434`)
- For remote servers, use a Tailscale address

## Built-in Rules

| Rule | Default | TTL | Description |
|------|---------|-----|-------------|
| OTP/verification codes | ✅ Enabled | 1 hour | Login codes, 2FA, email verification |
| Delivery confirmations | ✅ Enabled | 72 hours | UPS, FedEx, USPS "delivered" notices |
| Transit alerts | ❌ Disabled | 2 hours | CapMetro, MTA, BART delay alerts |
| Calendar reminders | ✅ Enabled | 24 hours | Google Calendar, Calendly reminders |
| Expires header (RFC) | ✅ Enabled | Per header | Honors the Expires header standard |
| AI promo scanner | ❌ Disabled | LLM-determined | Scans sale/promo emails for deadlines |

## Architecture

```
mailreaper/
├── manifest.json          # Extension manifest (MV3)
├── background.js          # Scanner service, alarm handlers, message router
├── rules/
│   ├── engine.js          # Rule evaluation logic (glob matching, TTL, LLM dispatch)
│   ├── storage.js         # Persistence layer (browser.storage.local)
│   └── defaults.js        # Built-in starter rules
├── llm/
│   ├── adapter.js         # Gemini/Ollama abstraction with JSON response parsing
│   └── prompts.js         # Analysis and rule-generation prompt templates
├── actions/
│   └── executor.js        # Move/delete/tag actions + grace period cleanup
├── options/               # Full settings UI (rules, LLM, folders, activity log)
├── popup/                 # Toolbar quick-status dashboard
├── icons/                 # SVG icons
└── _locales/en/           # i18n strings
```

## Development Notes

- **Manifest V3** with ES modules (`"type": "module"` in background)
- Uses `messenger.*` namespace (Thunderbird's alias for `browser.*`)
- All inter-component communication via `messenger.runtime.sendMessage()`
- LLM responses are cached by Message-ID header to avoid re-analysis
- Grace period cleanup runs on a separate 6-hour alarm

### Key APIs Used

- `messenger.messages.query()` — Find candidate messages by folder and date
- `messenger.messages.getFull()` — Access headers (including Message-ID, Expires)
- `messenger.messages.listInlineTextParts()` — Extract body text for LLM analysis
- `messenger.messages.move()` / `delete()` — Execute expiration actions
- `messenger.messages.tags.*` — Custom "Expired" tag management
- `messenger.folders.create()` — Auto-create the "Expired" folder
- `messenger.alarms.*` — Schedule scan and cleanup cycles
- `messenger.storage.local` — Persist rules, settings, cache, activity log

### Known Limitations / TODO

- [ ] Drag-to-reorder rules in the UI (wired in storage, needs DnD in options.js)
- [ ] "Create Rule from Examples" UI flow (LLM backend is ready in adapter.js)
- [ ] Per-rule folder scoping (data model supports it, UI doesn't expose it yet)
- [x] Undo support for individual actions
- [ ] Surface messages flagged as time-sensitive by LLM but without a parseable expiration date
- [ ] IMAP-specific edge cases (offline folders, slow connections)
- [ ] ATN submission and review
- [ ] Localization beyond English

## License

MIT
