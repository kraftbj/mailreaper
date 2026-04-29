# TODOs

## Add LLM source-phrase verification to deadline extraction (deferred from web-rewrite expiry redesign)

**What:** Augment the multi-classify and analysis prompts so that when the LLM returns a non-null `expiresAt`, it must also return the source phrase from the email body that justifies that date. The scanner verifies the quoted phrase actually appears in the message body before honoring the deadline. If the quote is absent or doesn't substring-match the body, drop the `expiresAt` and log it.

**Why:** The current 3A guard (reject `expiresAt` when LLM confidence < 0.7) catches low-confidence hallucinations but does nothing about high-confidence ones. LLMs occasionally invent dates with conviction. Without source-phrase verification, a message with no real deadline could get a fabricated `expiresAt`, sit in a triage folder, and get quietly swept to Expired weeks later for no traceable reason.

**Pros:**
- Strongest defense against hallucinated deadlines.
- Better debugging signal: log entries can show the exact phrase the LLM saw ("LLM said: 'sale ends 4/28/2026'").
- Future-proofs against model drift (a new Gemini version may hallucinate differently).

**Cons:**
- Larger prompt and more output tokens (modest cost increase per call).
- Substring matching against email bodies has fuzzy edges: HTML entities, whitespace differences, line wrapping, multipart MIME, plain-text fallbacks vs HTML body.
- Need to handle the case where the LLM returns a phrase that's a paraphrase rather than a literal quote — gracefully degrade or reject?

**Context:** This decision came out of `/plan-eng-review` for the web-rewrite expiry redesign in 2026-04. The design doc lives at `~/.gstack/projects/kraftbj-mailreaper/kraft-web-rewrite-design-20260429-081906.md`. We chose 3A (confidence threshold) over 3C (source-phrase verification) for the initial implementation because:
1. The user hadn't yet observed hallucinated-date failures in production.
2. 3A was 5 minutes of code; 3C is closer to an hour with edge-case handling.
3. We could observe whether 3A is sufficient before adding more weight.

**Trigger to revisit:** If you observe a message in Expired with no obvious deadline cause, or if activity log entries with `rule_name="Deferred expiry"` start appearing for messages that the user wouldn't consider expirable, that's the signal to implement 3C.

**Where to start:** `web/internal/llm/prompts.go` (add a `sourcePhrase` field to the JSON schema in both `BuildMultiClassificationPrompt` and `BuildAnalysisPrompt`); `web/internal/scanner/scanner.go` (post-parse: substring-check `result.SourcePhrase` against the body, drop `expiresAt` if absent). Body is already truncated to 2000 chars at scan time — verification should run against the same truncation to keep the test surface narrow.

**Depends on / blocked by:** Nothing structurally. Soft dependency on the web-rewrite expiry redesign landing first (this work assumes 3A is in place).

---

## Backfill verdicts produced under the pre-redesign expiry logic

**What:** Decide and execute a one-time cleanup for verdicts that were created under the old multi-classify prompt (which used "any past date = expired"). Three plausible strategies: (a) leave them, only apply new logic to new messages; (b) clear all non-executed verdicts and rescan; (c) clear all verdicts including executed ones and re-evaluate.

**Why:** Existing verdicts in the DB carry decisions that the new logic would reject. Some messages may have been wrongly moved to Expired and are now in the wrong folder.

**Pros:**
- Brings historical state in line with the new model.
- Reduces user confusion ("why did this digest end up in Expired three weeks ago?").

**Cons:**
- Strategy (c) is expensive: every message re-incurs an LLM call.
- Strategy (a) leaves artifacts; strategy (b) is in between but doesn't fix already-moved messages.
- None of (a)/(b)/(c) walks the Expired folder itself to rescue mistakenly-moved messages — that's a separate, harder operation requiring IMAP listing of the Expired folder and re-evaluation.

**Context:** Open Question #1 from the design doc. Cache is auto-flushed per 4A; this is the verdicts-table equivalent question. User owns the decision.

**Where to start:** `web/internal/db/verdicts.go` already has `ClearAllVerdicts()` at line 121. For walking Expired-folder content, would need a new IMAP path: list folder → fetch headers → re-evaluate against new rules → move misclassified messages back to inbox or to the right triage folder.

**Depends on / blocked by:** The expiry redesign landing first. Decide approach before next deploy.

---

## Consolidate body fetching between `evaluateMessage` and `evaluateMultiClassify`

**What:** `evaluateMessage` has a lazy `fetchBody` closure that caches the result in `bodyCache`. `evaluateMultiClassify` independently calls `client.FetchBody()` again. For messages that match a `content-regex` rule before reaching the multi-classify branch, the body gets fetched twice from IMAP.

**Why:** Cosmetic perf issue today (one redundant IMAP body fetch per multi-classified message). Not breaking anything; just wasteful.

**Pros:**
- Saves one IMAP round-trip per multi-classified message.
- Cleaner shared state between the two evaluation paths.

**Cons:**
- Requires plumbing the body cache through the function signature (or making it a struct field on a per-scan context).
- Pre-existing inefficiency, no urgency.

**Context:** Noticed during `/plan-eng-review` for the expiry redesign in 2026-04. Out of scope for that PR.

**Where to start:** `web/internal/scanner/scanner.go` — `evaluateMessage` and `evaluateMultiClassify`. Likely cleanest to introduce a small per-message context struct that carries the lazy body cache.

**Depends on / blocked by:** None.
