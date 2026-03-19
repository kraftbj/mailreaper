# MailReaper Privacy Policy

*Last updated: 2026-03-19*

## Overview

MailReaper is a Thunderbird extension that processes your email locally to identify and clean up time-sensitive messages. Your data stays on your machine except when you explicitly enable LLM-based rules, which send limited email metadata to an AI provider you choose.

## Data that stays local

All of the following remain entirely within Thunderbird on your device:

- Your email messages and attachments
- Rule configurations and settings
- Scan history and statistics
- LLM verdict caches (stored in extension local storage)
- Pattern-based rules (TTL, header, regex, classify) — these never contact any external service

## Data sent to external services

When you enable an LLM-powered rule (`llm` or `llm-classify` type), MailReaper sends the following to the LLM provider you configure:

- **Sender address** (From header)
- **Subject line**
- **Sent date**
- **Body snippet** (a truncated excerpt of the plain-text body)

This data is sent only for messages that match a rule's sender/subject/folder filters. It is not sent in bulk for all mail.

### Supported LLM providers

- **Google Gemini API** (`generativelanguage.googleapis.com`) — requires your own API key. Subject to [Google's API Terms of Service](https://ai.google.dev/gemini-api/terms).
- **Ollama** (self-hosted, local network) — runs on your own hardware. No data leaves your network.

MailReaper does not operate its own servers and does not collect, store, or transmit any data itself.

## API keys

Your Gemini API key is stored in Thunderbird's extension local storage on your device. It is sent only to Google's API endpoint and is never shared with MailReaper's developer or any other party.

## Permissions explained

| Permission | Why it's needed |
|---|---|
| `accountsRead` | List mail accounts to scan |
| `accountsFolders` | Access and create folders (e.g. "Expired") |
| `messagesRead` | Read message headers and body for rule evaluation |
| `messagesMove` | Move expired messages to the Expired folder |
| `messagesDelete` | Permanently delete messages after the grace period |
| `messagesTags` | Tag messages (e.g. mark as expired) |
| `storage` | Save settings, rules, and LLM verdict cache locally |
| `alarms` | Schedule periodic scan and cleanup cycles |
| `notifications` | Show scan completion notifications |
| `menus` | Add context menu items |
| Host: `generativelanguage.googleapis.com` | Call the Gemini API when Gemini is the configured provider |
| Optional: `<all_urls>` | Connect to a self-hosted Ollama instance at a user-specified URL |

## Data retention

- LLM verdict caches are stored locally with automatic expiration (24 hours for positive verdicts, 7 days for negative, 10 minutes for errors) and are capped at 5,000 entries.
- No data is retained by MailReaper outside of Thunderbird's extension storage.

## Changes

This policy may be updated as MailReaper evolves. Changes will be noted in the extension's release notes.

## Contact

For privacy questions, contact the developer via the project's homepage or repository.
