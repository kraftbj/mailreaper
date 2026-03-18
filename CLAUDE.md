# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

MailReaper is a Thunderbird extension (Manifest V3, ES modules) that automatically expires time-sensitive emails. It evaluates messages against prioritized rules (static pattern matching, RFC Expires header, LLM content analysis) and moves/deletes/tags expired ones.

## Development

**Load for testing:** Thunderbird → Tools → Add-ons → Debug Add-ons → Load Temporary Add-on → select `manifest.json`

No build step, no bundler, no package manager. Plain JS with ES module imports. The `messenger.*` namespace is Thunderbird's equivalent of `browser.*`.

Debug via Thunderbird's Add-on Developer Tools console (debug button next to the loaded extension).

## Architecture

**Scan cycle** (background.js orchestrates):
1. Alarm fires → `runScan()` walks configured folders
2. Each message evaluated against enabled rules sorted by priority (`rules/engine.js`)
3. First matching rule wins → verdict returned with `{ expired, expiresAt, reason, confidence }`
4. Actions executed (`actions/executor.js`): move to "Expired" folder, delete, or tag
5. Separate 6-hour alarm runs grace period cleanup (permanent deletion)

**Rule types** (`rules/engine.js` → `evaluateExpiration`):
- `ttl` — fixed hours after send date
- `header` — parses RFC `Expires` header
- `content-regex` — regex with capture group for date extraction
- `llm` — dispatches to LLM adapter, caches verdict by Message-ID

**Rule structure** (see `rules/defaults.js`): `match` object has `senderPatterns`, `subjectPatterns` (glob syntax), `folders`, `headerMatch`. `expiration` object specifies type and parameters.

**LLM layer** (`llm/adapter.js`): Gemini and Ollama backends behind a unified interface. Both request JSON responses. `llm/prompts.js` has the prompt templates. LLM verdicts are cached in `browser.storage.local` with 24h TTL, max 5000 entries.

**Storage** (`rules/storage.js`): All state in `messenger.storage.local` under `mailreaper_*` keys. Settings merge with defaults on read so new settings are picked up on extension updates.

**Inter-component communication**: All UI ↔ background communication goes through `messenger.runtime.sendMessage()` with a `type` field dispatch (see the `onMessage` listener in `background.js`).

## Key Conventions

- Glob patterns for sender/subject matching use `*` (any chars) and `?` (single char), implemented as regex conversion in `engine.js:globMatch()`
- Rule priority is numeric (lower = higher priority). Built-in Expires header rule is priority 1
- Built-in rules have `builtin: true` flag
- User-created rule IDs are prefixed `user-`
- The "Expired" folder is auto-created per account on first use
- LLM prompts instruct the model to prefer false negatives over false positives
