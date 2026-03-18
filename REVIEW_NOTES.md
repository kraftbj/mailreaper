# Review Notes — Items for User Decision

These items were flagged during automated review but require a design decision.

## Wontfix (accepted)

- **Gemini API key in plaintext storage** — inherent to browser extensions, no secure credential store in Thunderbird
- **innerHTML with hardcoded icons** — values are not user-controlled, practical risk is zero
- **ReDoS from user content-regex** — user-controlled, body truncated to 50k, regex validated at save time

## Needs Decision

### 1. Imputed LLM confidence (0.5) silently drops verdicts

When an LLM model omits the `confidence` field, it defaults to 0.5 which is below the 0.7 threshold, causing all verdicts from that model to be silently discarded. A `console.warn` and trace entry are logged, but the user may not notice. Options:
- Accept 0.5 as reasonable (current behavior)
- Treat missing confidence as 1.0 (trust the LLM)
- Treat missing confidence as an error (retry after 10m)
- Surface more prominently in the popup

### 2. `headerMatch` in rule definitions is dead code

The `match.headerMatch` field in rule definitions is never evaluated by `matchesRule()`. The builtin Expires header rule works because the header check happens in `evaluateExpiration`, but if a user creates a header-type rule, `headerMatch` gives the false impression of filtering. Options:
- Implement `headerMatch` checking in `matchesRule` (requires lazy full headers, performance cost)
- Remove `headerMatch` from the rule structure and options UI (simplest)
- Keep as-is and document that header matching happens in evaluation, not filtering

### 3. Storage leak of `mailreaper_movedAt_*` entries

When messages without `headerMessageId` are moved to Expired, no `movedAt` entry is created (and now those messages are never permanently deleted). Over time, messages without `headerMessageId` accumulate in the Expired folder. This is safer than premature deletion but may need a manual cleanup mechanism eventually.

### 4. OTP rule `gracePeriodDays: 0` allows permanent deletion within minutes

The default-enabled OTP rule has `gracePeriodDays: 0`. Combined with the 10-minute initial grace cleanup delay, a false positive (legitimate email with "verify your" in the subject) would be permanently deleted ~8 minutes after being moved. Options:
- Accept this as intentional for ephemeral OTP codes (current behavior)
- Set a minimum floor (e.g., 1 day) in cleanup
- Change OTP rule default to `gracePeriodDays: 1`
