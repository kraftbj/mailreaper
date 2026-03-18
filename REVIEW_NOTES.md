# Review Notes — Items for User Decision

These items were flagged during automated review but require a design decision.

## Wontfix (accepted)

- **Gemini API key in plaintext storage** — inherent to browser extensions, no secure credential store in Thunderbird
- **innerHTML with hardcoded icons** — values are not user-controlled, practical risk is zero
- **ReDoS from user content-regex** — user-controlled, body truncated to 50k, regex validated at save time
- **Storage leak of `mailreaper_movedAt_*` entries** — messages without `headerMessageId` accumulate in Expired rather than being permanently deleted. Safer than premature deletion; manual cleanup if needed.
- **Error response protocol between background and UI** — partial mitigation in place (options loaders check for `.error`). Full `{ ok, data }` protocol would be a significant refactor.

## Needs Decision

### 1. LLM says `isTimeSensitive: true` with no `expiresAt`

When the LLM identifies a message as time-sensitive but provides no expiration date, the verdict is silently treated as "not time-sensitive." The LLM's signal is lost. Could cache a flag like `timeSensitiveNoDate: true` and surface it in message info. Tracked as a TODO in README.
