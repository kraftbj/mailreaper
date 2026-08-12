# Review Findings Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all 24 P0/P1 findings from the 2026-08-12 multi-agent code review of the `web-rewrite` branch.

**Architecture:** Fixes land in seven phases ordered by blast radius: security first (the two P0s are reachable from any web page the user visits), then the expire-decision correctness path (these findings misfile real mail), then mail/DB state integrity, then the data layer, then the feedback loops that manufacture permanent rules, then reliability and UI, then the three destructive-path test gaps. Each task is independently testable and committable; phases can land separately.

**Tech Stack:** Go 1.22+ (`net/http` `ServeMux` with method+wildcard patterns), `modernc.org/sqlite` v1.48.1 (pure-Go, no cgo), `go-imap/v2`, vanilla ES modules for the UI (no bundler, no test runner).

**Spec:** This plan implements the review report produced by `compound-engineering:ce-code-review` run `20260812-151742-32ac7f8d`. Per-reviewer findings with full evidence are at `/tmp/compound-engineering/ce-code-review/20260812-151742-32ac7f8d/*.json`. Finding numbers below (`#N`) refer to the numbered P0/P1 table in that review.

## Global Constraints

- Go module path is `github.com/kraftbj/mailreaper`; all internal imports use that prefix.
- Every task ends green on `cd web && go build ./... && go vet ./... && go test ./...`.
- The system moves real mail. **Prefer false negatives over false positives** — a message left in the inbox is a non-event; a message wrongly filed or expired is a defect. When a fix could go either way, choose the side that acts less.
- Permanent deletion is currently a stub (`web/cmd/mailreaper/main.go:105`). Every finding here is presently bounded at "mail moved to the wrong folder". Findings #5, #13, #14, #15 and #17 escalate to irrecoverable loss the moment a grace sweep ships — do not ship that sweep before Phase 2 and Phase 5 land.
- Never use `git commit --amend`, `git push --force`, or `git rebase`. New commits only.
- Do not add AI attribution to commits, code comments, or PR bodies.
- Multi-line comment blocks in Go follow the file's existing convention: stacked `//` doc comments (this is idiomatic Go and the repo is uniform on it).
- `web/config.yaml` is gitignored and holds live credentials. Config schema changes must be made in `web/config.example.yaml` (tracked) **and** called out in the task so the operator updates their live file.

**Existing test fixtures** (all in package `scanner`, so no import is needed between them):
- `openTestDB(t *testing.T) *db.DB` — `scanner_test.go:51`. An identically named helper exists in package `db` at `rules_test.go:12`; they are separate.
- `mockMailClient` — `scanner_test.go:14`, a plain struct with fields `messages`, `movedMsgs` (records destination folders, not message IDs), `inboxMsgIDs`, `folderMessages`. There is no constructor; build it with a struct literal.
- `scanner.New(database *db.DB, cfg *config.Config) *Scanner` — `scanner.go:37`. Note it is `New`, not `NewScanner`.
- Test snippets below refer to `cfg`; reuse the `*config.Config` fixture the existing tests in the same file build.

## File Structure

| File | Responsibility | Tasks |
|---|---|---|
| `web/internal/server/middleware.go` | **New.** Same-origin / content-type guard for state-changing requests. | 1 |
| `web/internal/server/server.go` | Route registration, handler chain, listen address. | 1, 3, 17 |
| `web/internal/server/api.go` | REST handlers. | 16 |
| `web/ui/js/dashboard.js` | Dashboard rendering, scan button, mini review queue. | 2, 18 |
| `web/ui/js/review.js` | Review queue table and per-row actions. | 19 |
| `web/docker-compose.yml` | Deployment surface: port publishing, `TZ`. | 3, 6 |
| `web/internal/rules/engine.go` | Pure rule evaluation. | 4 |
| `web/internal/scanner/scanner.go` | Scan orchestration, verdict construction, deadline parsing. | 5, 6, 7, 8, 9, 14 |
| `web/internal/config/config.go` | Config schema, defaults, timezone resolution. | 3, 6 |
| `web/internal/scanner/backfill.go` | `--backfill` folder re-evaluation. | 10, 11 |
| `web/internal/db/db.go` | Connection setup, schema, one-shot migrations. | 12, 13 |
| `web/internal/db/llmcache.go` | LLM verdict cache. | 13 |
| `web/internal/db/verdicts.go` | Verdict CRUD. | 16 |
| `web/internal/scanner/distill.go` | Auto-rule generation from user behavior. | 15 |
| `web/cmd/mailreaper/main.go` | Wiring, cron, scan entry points. | 8, 17 |
| `web/internal/scanner/sweep_test.go` | **New.** Deferred-expiry sweep coverage. | 20 |

---

## Phase 1 — Security (P0s first)

### Task 1: Reject cross-origin state-changing requests (#1)

**Files:**
- Create: `web/internal/server/middleware.go`
- Modify: `web/internal/server/server.go:32-52` (ServeHTTP and Start must both use the guarded handler)
- Test: `web/internal/server/middleware_test.go` (new)

**Interfaces:**
- Produces: `withSameOrigin(next http.Handler) http.Handler`, and `Server.handler http.Handler` — the wrapped chain that both `ServeHTTP` and `Start` serve.

Context: there is no authentication on this API and it can create rules and trigger `/api/rescan`. `web/ui/js/app.js:13-18` sets `Content-Type: application/json` on **every** request including DELETE, so the bundled UI is unaffected. **It does break other clients:** any curl invocation or script that POSTs `/api/scan`, `/api/rescan`, or issues a DELETE without setting the header will now get a 403. That is an acceptable trade for closing the hole, but it is a real behavior change — document it in the README rather than claiming nothing is affected. A cross-origin HTML form cannot set it (forms are limited to `text/plain`, `multipart/form-data`, `application/x-www-form-urlencoded`) and cannot forge `Sec-Fetch-Site`, so the pair kills form-based CSRF. This is a mitigation, not authentication — real auth belongs behind a reverse proxy.

- [ ] **Step 1: Write the failing test**

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSameOriginGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	guarded := withSameOrigin(ok)

	tests := []struct {
		name        string
		method      string
		contentType string
		fetchSite   string
		wantStatus  int
	}{
		{"GET is never blocked", http.MethodGet, "", "cross-site", http.StatusOK},
		{"same-origin JSON POST allowed", http.MethodPost, "application/json", "same-origin", http.StatusOK},
		{"JSON POST with charset allowed", http.MethodPost, "application/json; charset=utf-8", "same-origin", http.StatusOK},
		{"no Sec-Fetch-Site (curl) allowed if JSON", http.MethodPost, "application/json", "", http.StatusOK},
		{"DELETE from UI allowed", http.MethodDelete, "application/json", "same-origin", http.StatusOK},
		{"cross-site JSON POST blocked", http.MethodPost, "application/json", "cross-site", http.StatusForbidden},
		{"form-encoded POST blocked", http.MethodPost, "application/x-www-form-urlencoded", "", http.StatusForbidden},
		{"text/plain POST blocked", http.MethodPost, "text/plain", "", http.StatusForbidden},
		{"missing content-type blocked", http.MethodPost, "", "", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/rules", strings.NewReader("{}"))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			rec := httptest.NewRecorder()
			guarded.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("got %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/server/ -run TestSameOriginGuard`
Expected: FAIL — `undefined: withSameOrigin`

- [ ] **Step 3: Write the middleware**

Create `web/internal/server/middleware.go`:

```go
package server

import (
	"mime"
	"net/http"
)

// withSameOrigin rejects state-changing requests that did not originate from
// the dashboard itself.
//
// The API has no authentication, so without this guard any page the user
// visits while the server is running can create rules or trigger a rescan.
// Two checks together defeat form-based CSRF: a cross-origin <form> cannot
// send Content-Type: application/json (forms may only send text/plain,
// multipart/form-data, or application/x-www-form-urlencoded), and it cannot
// forge Sec-Fetch-Site. The bundled UI already sends application/json on
// every mutation (web/ui/js/app.js), so no legitimate client is affected.
//
// This is defense against the browser-driven attack only. It is not
// authentication: any local process can still call the API directly. Real
// auth belongs in a reverse proxy in front of this server.
func withSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		// Browsers set Sec-Fetch-Site on every request. Absent means a
		// non-browser client (curl, a script), which CSRF does not apply to.
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
			// allowed
		default:
			jsonError(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}

		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "application/json" {
			jsonError(w, "expected Content-Type: application/json", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && go test ./internal/server/ -run TestSameOriginGuard -v`
Expected: PASS, all 9 subtests.

- [ ] **Step 5: Wire the middleware into both serve paths**

In `web/internal/server/server.go`, add a `handler http.Handler` field to `Server`, set it in `NewServer` after `registerRoutes()`, and serve it from both entry points. Both must change — `ServeHTTP` is what `api_test.go` exercises, `Start` is production:

```go
type Server struct {
	db                *db.DB
	mux               *http.ServeMux
	handler           http.Handler
	port              int
	OnScanRequested   func()
	OnRescanRequested func()
}

func NewServer(database *db.DB, port int) *Server {
	s := &Server{
		db:   database,
		mux:  http.NewServeMux(),
		port: port,
	}
	s.registerRoutes()
	s.handler = withSameOrigin(s.mux)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}
```

And in `Start`, change `Handler: s.mux` to `Handler: s.handler`.

- [ ] **Step 6: Fix existing tests that now 403**

Run: `cd web && go test ./internal/server/`
Existing mutation tests in `api_test.go` that build requests without a content type will now fail with 403. Add `req.Header.Set("Content-Type", "application/json")` to each. This is the correct signal — those tests were asserting behavior the guard now forbids.

- [ ] **Step 7: Verify the whole suite**

Run: `cd web && go build ./... && go vet ./... && go test ./...`
Expected: all packages ok.

- [ ] **Step 8: Commit**

```bash
git add web/internal/server/
git commit -m "fix: reject cross-origin state-changing API requests

The API has no authentication and can create rules and trigger a full
rescan. Any page the user visited while the server was running could
drive it with a cross-origin form POST.

Requires application/json plus a same-origin (or absent) Sec-Fetch-Site
on every non-GET request. A cross-origin form can send neither, and the
bundled UI already sends application/json on every mutation including
DELETE, so no legitimate client changes.

This is not authentication -- any local process can still call the API.
Real auth belongs in a reverse proxy."
```

---

### Task 2: Fix stored XSS in the dashboard (#2, #12)

**Files:**
- Modify: `web/ui/js/dashboard.js:53` (category icon), `web/ui/js/dashboard.js:100-110` (verdict action buttons)

**Interfaces:**
- Consumes: `esc()` — already defined in `dashboard.js` and used on adjacent fields.
- Produces: nothing other modules depend on.

Two defects at once. The icon is interpolated raw while `settings.js:26` escapes the same field. The action buttons are worse: `onclick="approveVerdict(${JSON.stringify(v.messageIdHeader)})"` emits `JSON.stringify`'s own double quotes *inside* a double-quoted HTML attribute, so the attribute terminates early and an attacker-chosen Message-ID becomes markup. A consequence worth noting: **these buttons cannot be working today for any message** — the emitted `onclick` is truncated at the first quote. `review.js:110-120` already uses the correct `data-` attribute plus delegated listener pattern; copy it.

- [ ] **Step 1: Verify the bug by hand before changing anything**

Run:

```bash
cd /Users/kraft/code/mailreaper && node -e '
const id = `<img src=x onerror=alert(1)>`;
console.log(`<button onclick="approveVerdict(${JSON.stringify(id)})">Approve</button>`);
'
```

Expected output shows the attribute closing immediately after `approveVerdict(` with the payload sitting in markup context. Record this; it is the reproduction.

- [ ] **Step 2: Escape the category icon**

In `web/ui/js/dashboard.js:53`, change:

```js
<div class="icon">${cat.icon || "📁"}</div>
```

to:

```js
<div class="icon">${esc(cat.icon) || "📁"}</div>
```

- [ ] **Step 3: Replace inline onclick with data attributes**

In `web/ui/js/dashboard.js`, replace the two action buttons:

```js
        <div class="verdict-actions">
          <button class="btn btn-sm btn-success" data-action="approve" data-id="${esc(v.messageIdHeader)}">Approve</button>
          <button class="btn btn-sm btn-danger"  data-action="reject"  data-id="${esc(v.messageIdHeader)}">Reject</button>
        </div>
```

- [ ] **Step 4: Attach one delegated listener**

After the review-queue container is populated, register a single listener (guard against double-registration since the container is re-rendered):

```js
const queueEl = document.getElementById("review-queue");
if (queueEl && !queueEl.dataset.listenerBound) {
  queueEl.dataset.listenerBound = "1";
  queueEl.addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-action]");
    if (!btn) return;
    const status = btn.dataset.action === "approve" ? "approved" : "rejected";
    updateVerdict(btn.dataset.id, status);
  });
}
```

- [ ] **Step 5: Verify in a real browser against the real server**

Do not verify by inspecting the template string. Insert a verdict whose Message-ID contains a payload, then load the dashboard:

```bash
cd web
sqlite3 mailreaper.db "INSERT INTO verdicts (account_id, message_id_header, subject, sender, sent_at, status, confidence, evaluated_at) VALUES ('test', '<img src=x onerror=console.log(\"XSS\")>@evil.test', 'XSS probe', 'a@b.test', datetime('now'), 'pending', 0.5, datetime('now'));"
./mailreaper &
```

Open `http://127.0.0.1:8025/`, confirm in DevTools that (a) no `XSS` line appears in the console, (b) the Message-ID renders as literal text, and (c) Approve/Reject now actually fire — they did not before this change. Then remove the probe row:

```bash
sqlite3 mailreaper.db "DELETE FROM verdicts WHERE account_id = 'test';"
```

- [ ] **Step 6: Commit**

```bash
git add web/ui/js/dashboard.js
git commit -m "fix: stop injecting attacker-controlled Message-ID into dashboard markup

approveVerdict(\${JSON.stringify(id)}) inside a double-quoted onclick
emitted JSON's own quotes into attribute context, so a Message-ID chosen
by the sending mail server became markup. It also meant the attribute
was truncated for every message, so the dashboard's approve and reject
buttons had never worked.

Switches to the data-attribute plus delegated-listener pattern already
used in review.js, and escapes the category icon the way settings.js
already does."
```

---

### Task 3: Bind to loopback by default (#19)

**Files:**
- Modify: `web/internal/config/config.go:54-57` (ServerConfig), `web/internal/config/config.go:82-95` (applyDefaults), `web/internal/server/server.go:38-52` (Start)
- Modify: `web/docker-compose.yml`, `web/config.example.yaml`
- Test: `web/internal/config/config_test.go`

The shipped compose file publishes an unauthenticated dashboard — which can read all mail metadata and move mail — to every interface. A bare `go run` binds `:8025`, which is also all interfaces.

- [ ] **Step 1: Write the failing test**

```go
func TestDefaultBindAddrIsLoopback(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.Server.BindAddr != "127.0.0.1" {
		t.Errorf("BindAddr = %q, want 127.0.0.1", cfg.Server.BindAddr)
	}
}

func TestExplicitBindAddrIsPreserved(t *testing.T) {
	cfg := &Config{Server: ServerConfig{BindAddr: "0.0.0.0"}}
	applyDefaults(cfg)
	if cfg.Server.BindAddr != "0.0.0.0" {
		t.Errorf("BindAddr = %q, want 0.0.0.0", cfg.Server.BindAddr)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/config/ -run BindAddr`
Expected: FAIL — `unknown field BindAddr`

- [ ] **Step 3: Add the field, default, and use it**

In `config.go`:

```go
type ServerConfig struct {
	Port     int    `yaml:"port"`
	BindAddr string `yaml:"bind_addr"`
}
```

In `applyDefaults`:

```go
	if cfg.Server.BindAddr == "" {
		cfg.Server.BindAddr = "127.0.0.1"
	}
```

In `server.go`, store the bind address on `Server` (add `bindAddr string`, set from a new `NewServer(database, bindAddr, port)` parameter or a settable field — prefer a parameter so it cannot be forgotten), and build the address with `net.JoinHostPort`:

```go
	addr := net.JoinHostPort(s.bindAddr, strconv.Itoa(s.port))
	httpSrv := &http.Server{Addr: addr, Handler: s.handler}
```

Update the `NewServer` call in `web/cmd/mailreaper/main.go:111` to pass `cfg.Server.BindAddr`.

- [ ] **Step 4: Run tests**

Run: `cd web && go build ./... && go test ./internal/config/ ./internal/server/`
Expected: PASS.

- [ ] **Step 5: Publish loopback-only in compose and document it**

`web/docker-compose.yml`: change the port mapping to `- "127.0.0.1:8025:8025"`.

`web/config.example.yaml`, under `server:`:

```yaml
server:
  # Port for the web UI and API (default: 8025)
  port: 8025
  # Interface to bind. Defaults to loopback because the dashboard has no
  # authentication and can move mail. Only change this behind a reverse
  # proxy that adds auth.
  bind_addr: 127.0.0.1
```

- [ ] **Step 6: Commit**

```bash
git add web/internal/config/ web/internal/server/ web/cmd/mailreaper/main.go web/docker-compose.yml web/config.example.yaml
git commit -m "fix: bind the dashboard to loopback by default

The dashboard has no authentication and can read all mail metadata and
move messages, but the shipped compose file published it on every
interface and a bare run bound :8025. Defaults to 127.0.0.1 with an
explicit bind_addr escape hatch for users who front it with a proxy."
```

---

## Phase 2 — The expire decision path

### Task 4: Make the Expires header rule actually fire (#3)

**Files:**
- Modify: `web/internal/rules/engine.go:129`
- Test: `web/internal/rules/engine_test.go:155-210`

`web/internal/imap/client.go:132-133` lowercases every header key when building `FetchedMessage.Headers`. `EvaluateHeader` looks up `headers["Expires"]`. The built-in priority-1 RFC `Expires` rule has therefore never fired in production. `engine_test.go` hand-builds its map with a capital `"Expires"`, which is exactly why this passed CI.

- [ ] **Step 1: Write the failing test using the real key convention**

Add to `engine_test.go`:

```go
// TestEvaluateHeaderUsesLowercaseKeys builds the headers map the way
// imap.FetchNewMessages builds it (client.go lowercases every key). The
// pre-existing tests in this file use a capital "Expires", which is why a
// rule that could never fire in production passed CI.
func TestEvaluateHeaderUsesLowercaseKeys(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour).Format(time.RFC1123Z)
	headers := map[string][]string{"expires": {past}}

	verdict := EvaluateHeader(headers, db.Rule{ID: "builtin-expires"})
	if verdict == nil {
		t.Fatal("expected a verdict for a past Expires header with a lowercase key")
	}
	if !verdict.Expired {
		t.Error("expected Expired = true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/rules/ -run TestEvaluateHeaderUsesLowercaseKeys -v`
Expected: FAIL — "expected a verdict for a past Expires header with a lowercase key"

- [ ] **Step 3: Look the header up case-insensitively**

Replace `engine.go:129`:

```go
	values, ok := headerValues(headers, "expires")
	if !ok || len(values) == 0 || values[0] == "" {
		return nil
	}
```

And add the helper below `EvaluateHeader`:

```go
// headerValues looks up a header case-insensitively. imap.FetchNewMessages
// lowercases every key when it builds the map, but callers and tests have
// historically used canonical MIME casing, so accept both.
func headerValues(headers map[string][]string, name string) ([]string, bool) {
	if v, ok := headers[name]; ok {
		return v, true
	}
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}
```

Add `"strings"` to the imports if not already present.

- [ ] **Step 4: Run tests to verify both key styles pass**

Run: `cd web && go test ./internal/rules/ -v`
Expected: PASS, including the pre-existing capital-`Expires` tests.

- [ ] **Step 5: Document the contract at the source**

In `web/internal/imap/client.go`, on the `Headers` field of `FetchedMessage`, add:

```go
	// Headers holds the message headers with all keys lowercased.
	Headers map[string][]string
```

- [ ] **Step 6: Commit**

```bash
git add web/internal/rules/engine.go web/internal/rules/engine_test.go web/internal/imap/client.go
git commit -m "fix: Expires header rule never matched due to key casing

The IMAP layer lowercases every header key when building the headers
map; EvaluateHeader looked up \"Expires\". The built-in priority-1 RFC
Expires rule -- the one standards-based, zero-cost rule in the system --
has never fired. The existing tests built the map by hand with canonical
casing, so this passed CI.

Looks the header up case-insensitively and documents the lowercase-key
contract on FetchedMessage.Headers."
```

---

### Task 5: Treat date-only deadlines as end of day (#5)

**Files:**
- Modify: `web/internal/scanner/scanner.go:356-400` (`parseExpiresAt`)
- Test: `web/internal/scanner/buildverdict_test.go`

`web/internal/llm/prompts.go` instructs the model to express a date-only deadline as end-of-day, but `parseExpiresAt` maps `2026-06-26` to `00:00:00`, expiring the message a full day before its stated deadline. This is the exact direction the codebase is built to avoid.

- [ ] **Step 1: Write the failing test**

Add to `buildverdict_test.go`:

```go
// TestParseExpiresAtDateOnlyIsEndOfDay pins the semantics prompts.go asks
// the model for: a bare date means the deadline lapses at the end of that
// day, not at midnight when it begins.
func TestParseExpiresAtDateOnlyIsEndOfDay(t *testing.T) {
	got, _ := parseExpiresAt("2026-06-26")
	if got == nil {
		t.Fatal("parseExpiresAt(\"2026-06-26\") = nil, want a time")
	}
	want := time.Date(2026, 6, 26, 23, 59, 59, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

// A date-only deadline for today must not read as already expired while
// that day is still in progress.
func TestParseExpiresAtDateOnlyTodayIsNotPast(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	_, isPast := parseExpiresAt(today)
	if isPast {
		t.Errorf("date-only deadline of today (%s) reported as already past", today)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/scanner/ -run TestParseExpiresAtDateOnly -v`
Expected: FAIL — got `00:00:00`, want `23:59:59`; and today's date reported past.

- [ ] **Step 3: Split the date-only layout out and advance it**

In `scanner.go`, remove `"2006-01-02"` from `zonelessExpiryLayouts` and handle it separately at the end of `parseExpiresAt`:

```go
// dateOnlyExpiryLayout is handled separately from the datetime layouts: a
// bare date means the deadline lapses at the END of that day. prompts.go
// asks the model for 23:59:59 semantics, and reading a bare date as
// midnight expires the message a full day early.
const dateOnlyExpiryLayout = "2006-01-02"

func parseExpiresAt(s string) (*time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}

	for _, layout := range zonedExpiryLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, time.Now().After(t)
		}
	}

	for _, layout := range zonelessExpiryLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return &t, time.Now().After(t)
		}
	}

	if t, err := time.ParseInLocation(dateOnlyExpiryLayout, s, time.Local); err == nil {
		// AddDate normalizes through the calendar and location. A fixed
		// 24h duration is WRONG across DST: on a 25-hour fall-back day it
		// lands at 22:59:59 (an hour early -- the direction that destroys
		// mail), and on a 23-hour spring-forward day it rolls into the
		// next calendar day.
		t = t.AddDate(0, 0, 1).Add(-time.Second)
		return &t, time.Now().After(t)
	}

	return nil, false
}
```

Remove `"2006-01-02"` from the `zonelessExpiryLayouts` slice so it cannot match first.

- [ ] **Step 4: Update the existing date-only assertion**

`TestParseExpiresAtLayouts` currently asserts `"2026-06-26"` equals local midnight. Change that case's `want` to `time.Date(2026, 6, 26, 23, 59, 59, 0, time.Local)` and rename the case to `"date only is end of local day"`.

- [ ] **Step 5: Run the suite**

Run: `cd web && go test ./internal/scanner/ -v -run TestParseExpiresAt`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/internal/scanner/
git commit -m "fix: date-only deadlines expired a day early

prompts.go asks the model to express a bare date as an end-of-day
deadline, but parseExpiresAt mapped 2026-06-26 to 00:00:00, so a message
whose offer ran through the 26th was expired at the start of the 26th.
Handles the date-only layout separately and advances it to 23:59:59."
```

---

### Task 6: Parse zoneless deadlines against a configured timezone (#18)

**Files:**
- Modify: `web/internal/config/config.go` (add `Timezone`, resolve it), `web/internal/scanner/scanner.go` (thread a `*time.Location`), `web/config.example.yaml`, `web/docker-compose.yml`
- Test: `web/internal/config/config_test.go`, `web/internal/scanner/buildverdict_test.go`

`parseExpiresAt` reads zoneless timestamps in `time.Local` on the assumption that the server's zone is the user's. `web/Dockerfile` and `web/docker-compose.yml` never set `TZ`, so a containerized deployment runs in UTC and reintroduces exactly the early-expiry bug commit `91a2f9b` was written to prevent. Make the assumption explicit and configurable.

- [ ] **Step 1: Write the failing config test**

```go
func TestResolveTimezone(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
		want    string
	}{
		{"", false, "Local"},
		{"Local", false, "Local"},
		{"America/Chicago", false, "America/Chicago"},
		{"UTC", false, "UTC"},
		{"Not/AZone", true, ""},
	}
	for _, tt := range tests {
		loc, err := ResolveTimezone(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ResolveTimezone(%q): expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ResolveTimezone(%q): %v", tt.in, err)
		}
		if loc.String() != tt.want {
			t.Errorf("ResolveTimezone(%q) = %q, want %q", tt.in, loc, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/config/ -run TestResolveTimezone`
Expected: FAIL — `undefined: ResolveTimezone`

- [ ] **Step 3: Add the config field and resolver**

In `config.go`, add to `Config`:

```go
	// Timezone names the IANA zone used to interpret LLM-extracted deadlines
	// that carry no UTC offset (the model does not reliably emit one). Empty
	// or "Local" uses the host zone, which is wrong inside a container: the
	// shipped image has no TZ set and would otherwise read every zoneless
	// deadline as UTC, expiring mail early for users behind UTC.
	Timezone string `yaml:"timezone"`
```

And:

```go
// ResolveTimezone turns a configured zone name into a *time.Location.
func ResolveTimezone(name string) (*time.Location, error) {
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("config: unknown timezone %q: %w", name, err)
	}
	return loc, nil
}
```

Add `"strings"` and `"time"` to the imports.

- [ ] **Step 4: Thread the location through the scanner**

Change the two parsing helpers to take a location, and add a `loc *time.Location` field to `Scanner` set at construction:

```go
func parseExpiresAt(s string, loc *time.Location) (*time.Time, bool)
func extractValidExpiresAt(expiresAt string, confidence float64, loc *time.Location) (parsed *time.Time, past bool, valid bool)
```

Replace every `time.ParseInLocation(layout, s, time.Local)` with `time.ParseInLocation(layout, s, loc)`. Update the two call sites in `scanner.go` (the multi-classify path and the `llm` path) to pass `s.loc`.

**Keep `New(database *db.DB, cfg *config.Config) *Scanner` unchanged.** Resolve the location *inside* `New` from `cfg.Timezone` rather than adding a parameter — every other task in this plan, and the shared test fixture contract, constructs the scanner as `New(d, cfg)`, and changing the signature here would silently invalidate all of them:

```go
func New(database *db.DB, cfg *config.Config) *Scanner {
	loc, err := config.ResolveTimezone(cfg.Timezone)
	if err != nil {
		log.Printf("scanner: %v; falling back to host local time", err)
		loc = time.Local
	}
	return &Scanner{db: database, cfg: cfg, loc: loc}
}
```

Validating the zone name at startup belongs in `config.Load` (Task 6 adds `ResolveTimezone`; call it there and fail fast), so a bad value is a startup error rather than a silent per-scanner fallback.

- [ ] **Step 5: Update the parse tests to pass a location**

Every `parseExpiresAt(x)` call in `buildverdict_test.go` becomes `parseExpiresAt(x, time.Local)`. Add one case proving the zone is honored:

```go
func TestParseExpiresAtHonorsConfiguredZone(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	got, _ := parseExpiresAt("2026-06-26T09:00:00", chicago)
	if got == nil {
		t.Fatal("expected a time")
	}
	want := time.Date(2026, 6, 26, 9, 0, 0, 0, chicago)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}
```

- [ ] **Step 6: Set TZ on the deployment surface**

`web/docker-compose.yml`, under the service:

```yaml
    environment:
      - TZ=${TZ:-UTC}
```

`web/config.example.yaml`, at top level:

```yaml
# IANA timezone used to interpret LLM-extracted deadlines that carry no UTC
# offset. Defaults to the host zone, which is UTC inside the container.
timezone: America/Chicago
```

- [ ] **Step 7: Run the suite and commit**

Run: `cd web && go build ./... && go vet ./... && go test ./...`

```bash
git add web/internal/config/ web/internal/scanner/ web/config.example.yaml web/docker-compose.yml
git commit -m "feat: configurable timezone for zoneless LLM deadlines

parseExpiresAt read zoneless timestamps in time.Local on the assumption
that the server's zone is the user's. The shipped Dockerfile and compose
file never set TZ, so a containerized deploy interpreted them as UTC and
reintroduced the early-expiry bug the zoneless parsing fix was written to
prevent. Makes the zone explicit in config and fails fast on a bad name."
```

---

### Task 7: Reject implausible LLM deadlines (#17)

**Files:**
- Modify: `web/internal/scanner/scanner.go` (`extractValidExpiresAt` and its two call sites)
- Test: `web/internal/scanner/buildverdict_test.go`

The only guard on an LLM-extracted deadline is `confidence >= 0.7`, which catches low-confidence hallucinations and nothing else. Production data shows the failure it misses: of 627 verdicts carrying a deadline, 7 expire *before* the message was sent and 25 more within an hour of it — including one case where the model returned the message's own send timestamp at confidence 1.00. A deadline earlier than the send date is impossible by construction; a deadline decades out is not a deadline. This is the cheap half of the deferred TODOS.md 3C work.

- [ ] **Step 1: Write the failing test**

```go
// TestExtractValidExpiresAtPlausibility rejects deadlines that cannot be
// real regardless of how confident the model claims to be. A deadline
// before the send date is impossible; one many years out is not a
// deadline. Both shapes appear in production data from the old model.
func TestExtractValidExpiresAtPlausibility(t *testing.T) {
	sentAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		expiresAt string
		wantValid bool
	}{
		{"a day after send is plausible", "2026-06-02T12:00:00Z", true},
		{"same instant as send is rejected", "2026-06-01T12:00:00Z", false},
		{"before send is rejected", "2026-05-01T12:00:00Z", false},
		{"one year out is plausible", "2027-05-01T12:00:00Z", true},
		{"ten years out is rejected", "2036-06-01T12:00:00Z", false},
	}
```

**Critical interaction with Task 5 — do not implement this as a calendar-date check.** The plausibility floor is an *instant* comparison (`t.After(sentAt)`), not "reject the same calendar day". A message sent at 6pm saying "offer ends today" yields a date-only deadline of 23:59:59 that same day, which is legitimately after the send instant and must be honored. Implementing the prose as "reject deadlines on the send date" would throw away a whole class of real same-day offers. Pin it with this test:

```go
// TestSameDayDeadlineSurvivesPlausibilityFloor guards the interaction
// between the end-of-day date parsing and the plausibility floor. A 6pm
// "offer ends today" is a real deadline, not an implausible one.
func TestSameDayDeadlineSurvivesPlausibilityFloor(t *testing.T) {
	sentAt := time.Date(2026, 6, 1, 18, 0, 0, 0, time.Local)
	_, past, valid := extractValidExpiresAt("2026-06-01", 1.0, time.Local, sentAt)
	if !valid {
		t.Fatal("same-day date-only deadline rejected; a 6pm \\"ends today\\" offer is real")
	}
	_ = past
}
```

Known edge: a message sent after 23:59:59 local (i.e. in the last second of the day) will have its "today" deadline rejected. Acceptable.

```go
	// remainder of TestExtractValidExpiresAtPlausibility

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, valid := extractValidExpiresAt(tt.expiresAt, 1.0, time.UTC, sentAt)
			if valid != tt.wantValid {
				t.Errorf("valid = %v, want %v", valid, tt.wantValid)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/scanner/ -run TestExtractValidExpiresAtPlausibility`
Expected: FAIL — too many arguments to `extractValidExpiresAt`.

- [ ] **Step 3: Add the plausibility window**

```go
// maxExpiryHorizon bounds how far past the send date an extracted deadline
// may sit. Beyond this the model is describing something other than a
// deadline on this message.
const maxExpiryHorizon = 2 * 365 * 24 * time.Hour

func extractValidExpiresAt(expiresAt string, confidence float64, loc *time.Location, sentAt time.Time) (parsed *time.Time, past bool, valid bool) {
	if expiresAt == "" {
		return nil, false, false
	}
	if confidence < minExpiryConfidence {
		log.Printf("scanner: dropping LLM expiresAt=%q with confidence=%.2f below %.2f threshold (likely hallucination)",
			expiresAt, confidence, minExpiryConfidence)
		return nil, false, false
	}
	t, isPast := parseExpiresAt(expiresAt, loc)
	if t == nil {
		log.Printf("scanner: dropping unparseable LLM expiresAt=%q (no layout matched)", expiresAt)
		return nil, false, false
	}
	if !sentAt.IsZero() {
		if !t.After(sentAt) {
			log.Printf("scanner: dropping implausible LLM expiresAt=%q -- at or before send date %s",
				expiresAt, sentAt.Format(time.RFC3339))
			return nil, false, false
		}
		if t.Sub(sentAt) > maxExpiryHorizon {
			log.Printf("scanner: dropping implausible LLM expiresAt=%q -- more than %.0f days after send date %s",
				expiresAt, maxExpiryHorizon.Hours()/24, sentAt.Format(time.RFC3339))
			return nil, false, false
		}
	}
	return t, isPast, true
}
```

Note this also closes the silent-drop gap the learnings researcher flagged: an unparseable value now logs instead of vanishing.

- [ ] **Step 4: Update both call sites**

Both call sites already have the message in scope. Pass `msg.Date`:

```go
	expiresAt, past, hasDeadline := extractValidExpiresAt(result.ExpiresAt, result.Confidence, s.loc, msg.Date)
```

Update the existing `TestExtractValidExpiresAt` cases to pass `time.UTC` and a zero `time.Time{}` for `sentAt` (the zero value disables the window, which is the documented behavior).

- [ ] **Step 5: Run the suite and commit**

Run: `cd web && go test ./internal/scanner/ -v`

```bash
git add web/internal/scanner/
git commit -m "fix: reject LLM deadlines that cannot be real

Confidence was the only guard on an extracted deadline, so it caught
low-confidence hallucinations and nothing else. Production data has 7
verdicts whose deadline precedes the send date and 25 more within an
hour of it, including one where the model returned the message's own
send timestamp at confidence 1.00.

Rejects deadlines at or before the send date and more than two years
after it, and logs unparseable values instead of dropping them silently."
```

---

### Task 8: Seed the canonical Expired category (#13)

**Files:**
- Modify: `web/internal/db/db.go` (one-shot migration), `web/internal/scanner/scanner.go:131-134` (fallback)
- Test: `web/internal/db/categories_test.go`

`canonicalExpiredFolder()` returns `""` unless a category whose ID is literally `expired` exists, and nothing seeds one. When it returns empty, past-deadline mail routes to the classify rule's own triage folder (Promotions, etc.) instead of Expired, and `SweepDeferredExpiries` returns early and never runs. Meanwhile `ScanAccount:132` falls back to the literal `"Expired"`, so the two paths disagree.

- [ ] **Step 1: Write the failing test**

```go
func TestExpiredCategorySeeded(t *testing.T) {
	d := openTestDB(t)

	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	for _, c := range cats {
		if c.ID == "expired" {
			if c.FolderName == "" {
				t.Error("expired category has an empty FolderName")
			}
			return
		}
	}
	t.Fatal("no category with ID \"expired\" was seeded; canonical Expired routing and the deferred sweep are both inert without it")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/db/ -run TestExpiredCategorySeeded`
Expected: FAIL — "no category with ID \"expired\" was seeded"

- [ ] **Step 3: Seed it in an idempotent one-shot migration**

Follow the existing `v2_expiry_redesign_cache_flush` pattern in `applyOneShotMigrations`. Add a migration keyed `v3_seed_expired_category`:

`GetCategories` (`categories.go:21`) SELECTs `id, name, folder_name, icon, color, created_at` and scans `created_at` into a `time.Time`. A three-column INSERT leaves `created_at` NULL and breaks every subsequent `GetCategories` call — including the one `canonicalExpiredFolder` makes. Populate the full row:

```sql
INSERT INTO categories (id, name, folder_name, icon, color, created_at)
VALUES ('expired', 'Expired', 'Expired', '⏰', '#888888', datetime('now'))
ON CONFLICT(id) DO NOTHING;
```

Re-read the DDL at `db.go:208` before running this and confirm the column list still matches.

- [ ] **Step 4: Make the fallback consistent and loud**

In `scanner.go`, replace the hard-coded fallback so it prefers the resolved folder:

```go
				dest := verdict.Rule.DestinationFolder
				if dest == "" {
					dest = expiredFolder
				}
				if dest == "" {
					dest = "Expired"
					log.Printf("scanner: no \"expired\" category configured; falling back to literal %q", dest)
				}
```

- [ ] **Step 5: Run tests and commit**

Run: `cd web && go test ./internal/db/ ./internal/scanner/`

```bash
git add web/internal/db/ web/internal/scanner/
git commit -m "fix: seed the canonical Expired category

canonicalExpiredFolder() keys on a category whose ID is literally
\"expired\" and nothing ever created one, so past-deadline mail routed to
the classify rule's own triage folder and SweepDeferredExpiries returned
early on every run. Seeds it in an idempotent one-shot migration and
makes the scanner's literal fallback log when it fires."
```

---

## Phase 3 — Mail and DB state must agree

### Task 9: A failed move must not be recorded as executed (#4)

**Files:**
- Modify: `web/internal/scanner/scanner.go:136-152`, `web/internal/scanner/feedback.go` (status guard)
- Test: `web/internal/scanner/scanner_test.go`

`EnsureFolder` and `MoveMessage` errors are logged and then execution continues: the verdict is saved `status:"executed"` with an `acted_at`, and the dedup check at `scanner.go:86-93` guarantees the message is never looked at again. The message stays in the inbox while the database claims it moved — permanently. Worse, `DetectFeedback` then reads the still-in-inbox message as a user correction.

**Design decision (settled).** Do *not* simply skip `SaveVerdict`. Saving nothing means the dedup check lets the next scan re-evaluate from scratch, which re-spends an LLM call on every scan for a permanently failing message. It also mishandles the ambiguous case where the IMAP MOVE actually succeeded before the error came back — with no verdict row, `DetectManualClassifications` later reads MailReaper's own move as a user filing.

Instead: persist a distinct `move_failed` status with no `acted_at`. Dedup skips it (no repeated LLM spend), a bounded retry pass can reclaim it, and the feedback detector ignores that status so an ambiguous success cannot be laundered.

- [ ] **Step 1: Add a failure seam to the mock**

The mock at `scanner_test.go:14` has no way to fail a move — its `MoveMessage` records the destination and returns nil unconditionally:

```go
type mockMailClient struct {
	messages       []imappkg.FetchedMessage
	movedMsgs      []string
	inboxMsgIDs    []string
	folderMessages map[string][]imappkg.FetchedMessage
	moveErr        error // when set, MoveMessage fails without recording
}

func (m *mockMailClient) MoveMessage(folder string, uid uint32, dest string) error {
	if m.moveErr != nil {
		return m.moveErr
	}
	m.movedMsgs = append(m.movedMsgs, dest)
	return nil
}
```

- [ ] **Step 2: Write the failing test**

Copy the rule and message fixture from the existing `TestScanCycleTTLRule` in the same file; the only difference is `moveErr`:

```go
// TestScanCycleFailedMoveRecordsMoveFailed verifies that a failed IMAP move
// is recorded as move_failed rather than executed. Recording executed is a
// permanent lie -- the dedup check means the message is never reconsidered,
// so the database disagrees with the mailbox forever. Recording nothing is
// also wrong: it re-spends an LLM call on every subsequent scan.
func TestScanCycleFailedMoveRecordsMoveFailed(t *testing.T) {
	d := openTestDB(t)
	client := &mockMailClient{
		moveErr: errors.New("IMAP MOVE failed: mailbox is read-only"),
	}
	// ... same rule + messages fixture as TestScanCycleTTLRule ...

	s := New(d, cfg)
	if err := s.ScanAccount(context.Background(), client, "acct", []string{"INBOX"}); err != nil {
		t.Fatalf("ScanAccount: %v", err)
	}

	if len(client.movedMsgs) != 0 {
		t.Errorf("recorded %d move(s) despite MoveMessage failing", len(client.movedMsgs))
	}

	v, err := d.GetVerdictByMessageID("<msg-1@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("no verdict saved; the next scan will re-spend an LLM call on this message")
	}
	if v.Status != "move_failed" {
		t.Errorf("Status = %q, want move_failed", v.Status)
	}
	if v.ActedAt != nil {
		t.Error("ActedAt set despite the move failing")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `cd web && go test ./internal/scanner/ -run TestScanCycleFailedMoveRecordsMoveFailed -v`
Expected: FAIL — `Status = "executed", want move_failed`.

- [ ] **Step 4: Record the failure instead of claiming success**

```go
			if verdict.Confidence >= 0.7 {
				dest := verdict.Rule.DestinationFolder
				if dest == "" {
					dest = expiredFolder
				}
				if dest == "" {
					dest = "Expired"
					log.Printf("scanner: no \"expired\" category configured; falling back to literal %q", dest)
				}

				// The move is authoritative. On failure the verdict is saved
				// as move_failed with no acted_at: dedup then skips it (so we
				// do not re-spend an LLM call every scan), a retry pass can
				// reclaim it, and DetectFeedback ignores the status so an
				// ambiguous success -- MOVE succeeded, error returned anyway --
				// is not read back as a user filing.
				moveErr := client.EnsureFolder(dest)
				if moveErr == nil {
					moveErr = client.MoveMessage(msg.Folder, msg.UID, dest)
				}
				if moveErr != nil {
					log.Printf("scanner: move %q to %q failed: %v; recording move_failed for retry", msg.MessageID, dest, moveErr)
					v.Status = "move_failed"
					v.DestinationFolder = dest
					if err := s.db.SaveVerdict(v); err != nil {
						log.Printf("scanner: save move_failed verdict for %q: %v", msg.MessageID, err)
					}
					continue
				}

				actedAt := time.Now().UTC()
				v.Status = "executed"
				v.ActedAt = &actedAt
				v.DestinationFolder = dest
				// ... unchanged SaveVerdict and LogActivity ...
			}
```

- [ ] **Step 5: Make the feedback detector ignore the new status**

In `web/internal/scanner/feedback.go`, `DetectManualClassifications` infers a user filing from a message sitting in a folder without a matching executed verdict. A `move_failed` verdict must suppress that inference — the ambiguous case is exactly where the move may have landed. Read the function, then exclude `move_failed` rows from the "no verdict" set it treats as user-placed.

Add a test asserting a `move_failed` verdict does not produce a manual-classification training example.

- [ ] **Step 6: Run the suite**

Run: `cd web && go test ./internal/scanner/ -v`
Expected: PASS, including the pre-existing scan-cycle tests.

- [ ] **Step 7: Commit**

```bash
git add web/internal/scanner/
git commit -m "fix: a failed IMAP move records move_failed, not executed

EnsureFolder and MoveMessage errors were logged and then ignored: the
verdict was saved as executed with an acted_at, and the dedup check at the
top of ScanAccount meant the message was never reconsidered. The message
stayed in the inbox while the database claimed it had moved.

Saving nothing instead would re-spend an LLM call on every scan for a
permanently failing message, and would let an ambiguous failure -- MOVE
succeeded, error returned anyway -- be read back as a user filing. A
distinct move_failed status with no acted_at is skipped by dedup, is
reclaimable by a retry pass, and is ignored by the feedback detector."
```


### Task 10: Backfill must not orphan verdicts or replay stale cache (#7, #8)

**Files:**
- Modify: `web/internal/scanner/backfill.go:105-116`
- Test: `web/internal/scanner/backfill_test.go`

Two defects at the same site. Backfill deletes the verdict before evaluating, then `continue`s on an LLM error — leaving a message with no verdict at all, which the next scan reads as a user manual filing. And it clears the verdict but *not* the cached LLM verdict, so `--backfill` — the operator's remediation tool — replays the exact stale old-model output it exists to refresh. The delete is also unnecessary: the dedup check lives in `ScanAccount`, not `evaluateMessage`, and backfill calls `evaluateMessage` directly.

- [ ] **Step 1: Verify the dedup claim before relying on it**

Run: `cd web && grep -n "GetVerdictByMessageID" internal/scanner/*.go`
Confirm the only dedup check is in `ScanAccount` and that `evaluateMessage` has none. If `evaluateMessage` does check, stop and revise this task — the delete would then be load-bearing.

- [ ] **Step 2: Write the failing test**

```go
// TestBackfillClearsCachedVerdict proves --backfill actually re-asks the
// LLM. The cache is keyed on Message-ID with no model component, so
// without an explicit removal, backfill replays the stale verdict it was
// run to refresh.
func TestBackfillClearsCachedVerdict(t *testing.T) {
	d := openTestDB(t)
	if err := d.SetCachedVerdict("<msg-1@test>", db.CachedVerdict{
		Reason:     "stale verdict from the previous model",
		Confidence: 0.9,
	}); err != nil {
		t.Fatalf("SetCachedVerdict: %v", err)
	}

	// ... run BackfillFolders over a folder containing <msg-1@test> ...

	cached, err := d.GetCachedVerdict("<msg-1@test>")
	if err != nil {
		t.Fatalf("GetCachedVerdict: %v", err)
	}
	if cached != nil && cached.Reason == "stale verdict from the previous model" {
		t.Error("backfill reused the stale cached verdict instead of re-evaluating")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd web && go test ./internal/scanner/ -run TestBackfillClearsCachedVerdict -v`
Expected: FAIL — stale verdict reused.

- [ ] **Step 4: Drop the verdict delete, remove the cache entry instead**

Replace `backfill.go:105-109`:

```go
			// Backfill re-asks the LLM rather than trusting a prior answer,
			// so the cached verdict must go. The verdict row itself is left
			// alone: the dedup check lives in ScanAccount, not
			// evaluateMessage, so deleting it here bought nothing and left a
			// hole that DetectManualClassifications misread as a user filing whenever the
			// evaluation below failed.
			//
			// NOTE: the misreading function is DetectManualClassifications
			// (feedback.go), which treats a missing verdict as "the user filed
			// this by hand". DetectFeedback is a different function that looks
			// for previously-executed verdicts reappearing in the inbox.
			if err := s.db.RemoveCachedVerdict(msg.MessageID); err != nil {
				log.Printf("backfill: remove cached verdict for %q: %v", msg.MessageID, err)
			}
```

- [ ] **Step 5: Run tests and commit**

Run: `cd web && go test ./internal/scanner/ -v`

```bash
git add web/internal/scanner/
git commit -m "fix: backfill re-asks the LLM and stops orphaning verdicts

Backfill deleted the verdict row before evaluating and then continued on
an LLM error, leaving a message with no verdict that the next scan read
as a user manual filing. The delete was also unnecessary -- the dedup
check is in ScanAccount, not evaluateMessage.

It also never cleared the cached LLM verdict, so the operator's
remediation tool replayed the stale answer it was run to refresh."
```

---

### Task 11: A Gemini outage must not empty triage into the inbox (#14)

**Files:**
- Modify: `web/internal/scanner/backfill.go:118-136`
- Test: `web/internal/scanner/backfill_test.go`

When `evaluateMessage` returns a nil verdict — which includes every LLM failure — backfill assigns `confidence := 1.0` and routes to `rescueFolder`. One Gemini outage during a `--backfill` run therefore empties every triage folder into the INBOX at full confidence, and the next scan reads that flood as user corrections, which `DistillRules` then turns into permanent rules.

- [ ] **Step 1: Write the failing test**

```go
// TestBackfillNilVerdictDoesNotRescue verifies that "we learned nothing"
// is not treated as "confidently belongs in the inbox". A nil verdict
// covers LLM failures, so the old behavior meant one API outage emptied
// every triage folder into the inbox at confidence 1.0.
func TestBackfillNilVerdictDoesNotRescue(t *testing.T) {
	d := openTestDB(t)
	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {{
				MessageID: "<msg-1@test>",
				Subject:   "unclassifiable",
				Sender:    "someone@example.test",
				Date:      time.Now().Add(-72 * time.Hour),
				Folder:    "Folders/AI-Triage/Promotions",
				UID:       1,
			}},
		},
	}
	// Seed no rules, so evaluateMessage returns a nil verdict for it.

	s := New(d, cfg)
	// Signature: (s *Scanner) BackfillFolders(ctx, client, accountID, rescueFolder)
	stats, err := s.BackfillFolders(context.Background(), client, "acct", "INBOX")
	if err != nil {
		t.Fatalf("BackfillFolders: %v", err)
	}
	if stats.Moved != 0 {
		t.Errorf("Moved = %d, want 0: a nil verdict must not move mail", stats.Moved)
	}
	if len(client.movedMsgs) != 0 {
		t.Errorf("moved %d message(s) on a nil verdict", len(client.movedMsgs))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/scanner/ -run TestBackfillNilVerdictDoesNotRescue -v`
Expected: FAIL — Moved = 1.

- [ ] **Step 3: Treat a nil verdict as no information**

Replace `backfill.go:119-122`:

```go
			dest := backfillDestination(verdict, expiredFolder, rescueFolder)
			// A nil verdict means "no rule matched or the LLM call failed" --
			// that is an absence of information, not confidence that the
			// message belongs in the inbox. Scoring it 0 lets the existing
			// <0.7 gate below leave the message where it is.
			confidence := 0.0
			if verdict != nil {
				confidence = verdict.Confidence
			}
```

- [ ] **Step 4: Run tests and commit**

Run: `cd web && go test ./internal/scanner/ -v`

```bash
git add web/internal/scanner/
git commit -m "fix: a nil backfill verdict no longer rescues at full confidence

evaluateMessage returns nil for LLM failures as well as no-match, and
backfill scored that 1.0 and moved the message to the rescue folder. One
Gemini outage during --backfill would empty every triage folder into the
inbox, and the next scan would read the flood as user corrections and
distill them into permanent rules."
```

---

## Phase 4 — Data layer

### Task 12: Actually apply the SQLite pragmas (#9)

**Files:**
- Modify: `web/internal/db/db.go:75-101`
- Test: `web/internal/db/db_test.go`

`modernc.org/sqlite` does not recognize `_journal_mode` or `_busy_timeout` DSN parameters — it uses `_pragma=name(value)`. And the standalone `PRAGMA foreign_keys = ON` applies only to whichever pooled connection served it. WAL, the busy timeout, and foreign-key enforcement are all off despite the doc comment claiming otherwise. This was reproduced against the pinned driver version.

- [ ] **Step 1: Write the failing test**

```go
// TestOpenAppliesPragmasToPooledConnections asserts the settings Open's doc
// comment promises. modernc.org/sqlite ignores _journal_mode/_busy_timeout
// DSN params, and a one-off PRAGMA Exec only configures the single
// connection that ran it, so this must be checked on a fresh connection.
func TestOpenAppliesPragmasToPooledConnections(t *testing.T) {
	d := openTestDB(t)

	// Hold DISTINCT connections open simultaneously. A loop of QueryRow on
	// the pool proves nothing -- it can hand back the same connection every
	// time, which is exactly the connection a one-off setup PRAGMA would
	// have configured. Only concurrently-held conns force the pool to open
	// new ones.
	ctx := context.Background()
	var conns []*sql.Conn
	for i := 0; i < 3; i++ {
		c, err := d.Conn(ctx)
		if err != nil {
			t.Fatalf("open conn %d: %v", i, err)
		}
		defer c.Close()
		conns = append(conns, c)
	}

	for i, c := range conns {
	for i, c := range conns {
		var journal string
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatalf("conn %d journal_mode: %v", i, err)
		}
		if !strings.EqualFold(journal, "wal") {
			t.Errorf("conn %d journal_mode = %q, want wal", i, journal)
		}

		var busy int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("conn %d busy_timeout: %v", i, err)
		}
		if busy != 5000 {
			t.Errorf("conn %d busy_timeout = %d, want 5000", i, busy)
		}

		var fk int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn %d foreign_keys: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("conn %d foreign_keys = %d, want 1", i, fk)
		}
	}
}
```

This test belongs in `internal/db`, whose `openTestDB` (`rules_test.go:12`) opens a **real temp file** via `t.TempDir()`. That matters: WAL is impossible on an in-memory database, so the same assertion written against `internal/scanner`'s `openTestDB` — which uses `:memory:` (`scanner_test.go:53`) — would fail for a reason unrelated to the bug.

The scanner package still opens `:memory:` with the new DSN appended. Requesting WAL on an in-memory database is a no-op in SQLite rather than an error, so those tests should be unaffected — verify that explicitly in Step 4 rather than assuming it.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/db/ -run TestOpenAppliesPragmasToPooledConnections -v`
Expected: FAIL — `journal_mode = "delete"`, `busy_timeout = 0`, `foreign_keys = 0`.

- [ ] **Step 3: Use the driver's real pragma syntax**

```go
	// modernc.org/sqlite takes per-connection pragmas as _pragma=name(value);
	// it silently ignores the _journal_mode / _busy_timeout spellings used by
	// mattn/go-sqlite3. Setting them in the DSN applies them to every pooled
	// connection, which a one-off PRAGMA Exec does not.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
```

Delete the standalone `PRAGMA foreign_keys = ON` Exec block — the DSN now covers it on every connection.

- [ ] **Step 4: Run tests**

Run: `cd web && go test ./internal/db/ -v`
Expected: PASS. Note that foreign keys are now genuinely enforced for the first time; if any existing test inserts a row with a dangling reference it will now fail. Fix the fixture rather than disabling the pragma.

- [ ] **Step 5: Commit**

```bash
git add web/internal/db/
git commit -m "fix: SQLite pragmas were never applied

modernc.org/sqlite ignores the _journal_mode and _busy_timeout DSN
parameters -- it wants _pragma=name(value) -- and the standalone PRAGMA
foreign_keys Exec configured only whichever pooled connection served it.
WAL, the 5s busy timeout, and foreign-key enforcement were all off
despite Open's doc comment promising them."
```

---

### Task 13: Redesign the LLM cache key and cache the category (#11, #6)

**Files:**
- Modify: `web/internal/db/db.go` (base schema + one-shot migration), `web/internal/db/llmcache.go`, `web/internal/scanner/scanner.go` (both LLM call sites)
- Test: `web/internal/db/llmcache_test.go`, `web/internal/scanner/scanner_test.go`

This task merges what were originally two tasks. They cannot be separated: both change the same table, and doing them as two migrations against the same PK is what made the original plan wrong.

Three defects in one place:

1. The cache keys on `message_id_header` alone with no model component, so a model swap leaves the previous model's verdicts authoritative (#11).
2. `evaluateMultiClassify` never uses the cache at all, so a message that does not cleanly classify is re-sent to the LLM every scan for the whole lookback window (#6).
3. The two prompt paths (`analysis` and `classify`) produce different answers for the same Message-ID. With one row per Message-ID they would **clobber each other**, and a model-mismatch-deletes-the-row rule would make them delete each other. Putting the prompt kind into the model string does not fix this — the primary key is what collides.

Additionally `CachedVerdict` has no `Category` field (`llmcache.go:16`) but `buildClassifyVerdict` needs `LLMResponse.Category` (`scanner.go:452`), so a cached classify hit cannot reconstruct a classification even if it were stored.

**The fix:** a composite primary key `(message_id_header, cache_key)` where `cache_key` is `"<provider>:<model>|<prompt-kind>"`, plus a `Category` field on `CachedVerdict`. SQLite cannot alter a primary key in place, so the migration rebuilds the table.

- [ ] **Step 1: Write the failing tests**

```go
func TestCacheKeyScopesModelAndPromptKind(t *testing.T) {
	d := openTestDB(t)

	analysisKey := "gemini:gemini-3.5-flash-lite|analysis"
	classifyKey := "gemini:gemini-3.5-flash-lite|classify"

	if err := d.SetCachedVerdict("<m@test>", analysisKey, db.CachedVerdict{Reason: "deadline answer"}); err != nil {
		t.Fatalf("SetCachedVerdict(analysis): %v", err)
	}
	if err := d.SetCachedVerdict("<m@test>", classifyKey, db.CachedVerdict{Reason: "classify answer", Category: "promotion", Classified: true}); err != nil {
		t.Fatalf("SetCachedVerdict(classify): %v", err)
	}

	// The two prompt kinds must coexist, not clobber.
	a, err := d.GetCachedVerdict("<m@test>", analysisKey)
	if err != nil {
		t.Fatalf("GetCachedVerdict(analysis): %v", err)
	}
	if a == nil || a.Reason != "deadline answer" {
		t.Errorf("analysis entry = %+v, want the deadline answer -- classify clobbered it", a)
	}

	c, err := d.GetCachedVerdict("<m@test>", classifyKey)
	if err != nil {
		t.Fatalf("GetCachedVerdict(classify): %v", err)
	}
	if c == nil || c.Category != "promotion" {
		t.Errorf("classify entry = %+v, want Category promotion -- a cached hit must reconstruct the classification", c)
	}

	// A different model is a miss, not a hit.
	old, err := d.GetCachedVerdict("<m@test>", "gemini:gemini-2.5-flash|analysis")
	if err != nil {
		t.Fatalf("GetCachedVerdict(old model): %v", err)
	}
	if old != nil {
		t.Errorf("got a hit across a model change: %+v", old)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd web && go test ./internal/db/ -run TestCacheKeyScopesModelAndPromptKind`
Expected: FAIL — too many arguments to `SetCachedVerdict`.

- [ ] **Step 3: Add Category to the cached record**

```go
type CachedVerdict struct {
	Classified bool    `json:"classified,omitempty"`
	Category   string  `json:"category,omitempty"`
	ExpiresAt  string  `json:"expiresAt,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Error      string  `json:"error,omitempty"`
}
```

- [ ] **Step 4: Update the base schema for fresh databases**

In the `CREATE TABLE IF NOT EXISTS llm_cache` statement in `db.go`, define the new shape directly:

```sql
CREATE TABLE IF NOT EXISTS llm_cache (
    message_id_header TEXT NOT NULL,
    cache_key         TEXT NOT NULL DEFAULT '',
    verdict           TEXT NOT NULL,
    cached_at         TIMESTAMP NOT NULL,
    PRIMARY KEY (message_id_header, cache_key)
)
```

- [ ] **Step 5: Write a migration that is safe on BOTH fresh and existing databases**

This is the step the first draft of this plan got wrong. A fresh database already has the new shape from Step 4, so an unconditional `ALTER`/rebuild fails. `applyOneShotMigrations` runs at `db.go:234`, *after* the `CREATE TABLE` block — the `schema_meta` key alone does not tell you whether the column exists. Probe the actual schema first:

```go
// v4_llm_cache_composite_key rebuilds llm_cache with a (message_id_header,
// cache_key) primary key so the analysis and classify prompt paths, and
// answers from different models, can coexist instead of overwriting each
// other. Fresh databases already have this shape from the base schema, so
// the migration probes for the column before doing anything.
func (d *DB) migrateLLMCacheCompositeKey() error {
	var hasCacheKey bool
	rows, err := d.Query(`PRAGMA table_info(llm_cache)`)
	if err != nil {
		return fmt.Errorf("probe llm_cache schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan llm_cache schema: %w", err)
		}
		if name == "cache_key" {
			hasCacheKey = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate llm_cache schema: %w", err)
	}
	if hasCacheKey {
		return nil // fresh DB, or already migrated
	}

	// Existing rows came from a single model and the analysis path. Drop them
	// rather than guessing a cache_key: they are at most 7 days of cached
	// answers, and a wrong key would serve one prompt path another's answer.
	_, err = d.Exec(`
		DROP TABLE IF EXISTS llm_cache;
		CREATE TABLE llm_cache (
			message_id_header TEXT NOT NULL,
			cache_key         TEXT NOT NULL DEFAULT '',
			verdict           TEXT NOT NULL,
			cached_at         TIMESTAMP NOT NULL,
			PRIMARY KEY (message_id_header, cache_key)
		);
	`)
	if err != nil {
		return fmt.Errorf("rebuild llm_cache: %w", err)
	}
	return nil
}
```

Call it from `applyOneShotMigrations` guarded by its `schema_meta` key, matching the existing `v2_expiry_redesign_cache_flush` pattern.

- [ ] **Step 6: Key every read and write on the pair**

`GetCachedVerdict(messageIDHeader, cacheKey string)` and `SetCachedVerdict(messageIDHeader, cacheKey string, cv CachedVerdict)` both take the key and use `WHERE message_id_header = ? AND cache_key = ?`. The upsert target becomes `ON CONFLICT(message_id_header, cache_key)`. There is no model-mismatch delete — a different key is simply a different row, which is the whole point.

- [ ] **Step 7: Build the key in the scanner**

```go
// llmCacheKey identifies which model and which prompt produced a cached
// answer. Both are required: the analysis and classify prompts return
// different shapes for the same message, and a model change invalidates
// both.
func (s *Scanner) llmCacheKey(promptKind string) string {
	var model string
	switch strings.ToLower(s.cfg.LLM.Provider) {
	case "gemini":
		model = "gemini:" + s.cfg.LLM.Gemini.Model
	case "ollama":
		model = "ollama:" + s.cfg.LLM.Ollama.Model
	default:
		model = s.cfg.LLM.Provider
	}
	return model + "|" + promptKind
}
```

`evaluateLLM` uses `s.llmCacheKey("analysis")`. `evaluateMultiClassify` uses `s.llmCacheKey("classify")` and gains the cache read/write it never had (#6): check the cache first, and on a miss call the LLM and store the result — including `Category` and the error case with `CachedVerdict{Error: err.Error()}` so a failing message backs off for the 10-minute error TTL.

- [ ] **Step 8: Verify the classify cache round-trips a real category**

Add a scanner-level test that seeds a classify cache entry with `Category: "promotion"`, runs a scan with an LLM stub that fails the test if called, and asserts the message is routed to the promotions folder. A cached hit that cannot reconstruct the category is the failure this guards.

- [ ] **Step 9: Run the suite and commit**

Run: `cd web && go build ./... && go vet ./... && go test ./...`

```bash
git add web/internal/db/ web/internal/scanner/
git commit -m "fix: give the LLM cache a composite key and cache the category

Three defects in one table. The cache keyed on Message-ID alone, so a
model swap left the old model's verdicts authoritative. The multi-classify
path never used the cache at all, re-sending unclassifiable messages to
the LLM on every scan for the whole lookback window. And both prompt paths
map to the same Message-ID, so storing them in one row makes them clobber
each other -- putting the prompt kind in a value column does not help when
the primary key is what collides.

Rebuilds the table with a (message_id_header, cache_key) primary key where
cache_key carries provider, model, and prompt kind, and adds the Category
field a cached classify hit needs to reconstruct its classification. The
migration probes PRAGMA table_info first so it is safe on fresh databases,
which already have the new shape from the base schema."
```


## Phase 5 — Feedback loops that manufacture rules

### Task 15: Distill rules on the exact sender, not the domain (#15)

**Files:**
- Modify: `web/internal/scanner/distill.go:150-158`
- Test: `web/internal/scanner/distill_test.go` (create if absent)

Filing three messages from one friend by hand generates a permanent `*@gmail.com` rule that silently files all personal mail out of the inbox. The evidence is three messages from one address; the rule generalizes to an entire consumer mail provider.

- [ ] **Step 1: Write the failing test**

```go
// TestDistillDoesNotWidenToConsumerDomain pins the evidence-to-rule
// relationship: three messages from one address justify a rule about that
// address, not about every user of their mail provider.
func TestDistillDoesNotWidenToConsumerDomain(t *testing.T) {
	// Signature is buildAutoRule(email string) (ruleID, senderPattern, name string)
	// -- note the return order.
	_, senderPattern, _ := buildAutoRule("friend@gmail.com")
	if senderPattern == "*@gmail.com" {
		t.Fatalf("distilled a provider-wide rule %q from a single sender", senderPattern)
	}
	if senderPattern != "friend@gmail.com" {
		t.Errorf("senderPattern = %q, want the exact sender address", senderPattern)
	}
}

// The SimpleLogin branch is per-sender by construction and must keep working.
func TestDistillSimpleLoginAliasUnchanged(t *testing.T) {
	_, senderPattern, _ := buildAutoRule("news_at_example_com_abc123@simplelogin.co")
	if !strings.HasSuffix(senderPattern, "@simplelogin.co") {
		t.Errorf("senderPattern = %q, want a simplelogin-scoped pattern", senderPattern)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/scanner/ -run TestDistillDoesNotWidenToConsumerDomain -v`
Expected: FAIL — distilled `*@gmail.com`.

- [ ] **Step 3: Match the exact address**

Replace the regular-email branch:

```go
	// Match the exact address. Three messages from one person is evidence
	// about that person, not about everyone who uses their mail provider --
	// a *@gmail.com rule silently files all personal mail out of the inbox.
	// The SimpleLogin branch above is different: those aliases are
	// per-sender by construction, so the pattern there is already specific.
	name = email
	senderPattern = email
	ruleID = fmt.Sprintf("auto-%s", sanitizeID(email))
	return
```

This makes the regular-email branch identical to the existing fallback, so collapse the two.

- [ ] **Step 4: Run tests and commit**

Run: `cd web && go test ./internal/scanner/ -v`

```bash
git add web/internal/scanner/
git commit -m "fix: distill auto-rules on the exact sender, not the domain

Filing three messages from one friend generated a permanent *@gmail.com
rule that silently filed all personal mail out of the inbox. Three
messages from one address are evidence about that address."
```

---

### Task 16: Stop rescan laundering its own moves, and split off a cheap refresh (#16)

**Files:**
- Modify: `web/internal/scanner/feedback.go` (the laundering guard), `web/internal/server/api.go`, `web/internal/server/server.go` (new route), `web/internal/db/verdicts.go` (new method), `web/ui/js/dashboard.js` (optional control)
- Test: `web/internal/server/api_test.go`, `web/internal/scanner/feedback_test.go`

`handleRescan` calls `ClearAllVerdicts()`, deleting every row including `executed` ones. `DetectManualClassifications` then finds messages sitting in triage folders with no verdict and concludes the *user* put them there, and `DistillRules` turns that into permanent rules — MailReaper laundering its own past decisions into training data.

**Design decision (settled).** The first draft fixed this by clearing only pending verdicts. That is wrong as a redefinition of `/api/rescan`: `ScanAccount` skips any message with an existing verdict *before* evaluating, so preserving executed rows means a rescan can never re-evaluate messages it already acted on — stale decisions from old rules would be frozen forever, and the endpoint would no longer do what its name promises.

Instead, keep `/api/rescan` fully destructive and honest, and fix the laundering at its actual source: the inference in `DetectManualClassifications`. Add a separate cheap endpoint for the common case.

- [ ] **Step 1: Write the failing laundering test**

```go
// TestRescanDoesNotLaunderOwnMoves is the real bug: after a full rescan
// clears verdicts, messages MailReaper itself moved are sitting in triage
// folders with no verdict row. Inferring "the user filed these" from that
// absence turns MailReaper's own decisions into training data, which
// DistillRules then promotes to permanent rules.
func TestRescanDoesNotLaunderOwnMoves(t *testing.T) {
	d := openTestDB(t)
	// Record that MailReaper placed <auto@test> in the promotions folder,
	// then clear all verdicts the way handleRescan does.
	// ... seed placement record, then d.ClearAllVerdicts() ...

	s := New(d, cfg)
	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {{MessageID: "<auto@test>", UID: 1}},
		},
	}
	if err := s.DetectManualClassifications(client, "acct"); err != nil {
		t.Fatalf("DetectManualClassifications: %v", err)
	}

	examples, err := d.GetTrainingExamples("promotion")
	if err != nil {
		t.Fatalf("GetTrainingExamples: %v", err)
	}
	for _, ex := range examples {
		if ex.Subject == "auto-moved message" {
			t.Error("MailReaper's own move was recorded as a user classification")
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd web && go test ./internal/scanner/ -run TestRescanDoesNotLaunderOwnMoves -v`
Expected: FAIL — the move was recorded as a user classification.

- [ ] **Step 3: Keep a durable record of MailReaper's own placements**

Add a `placements` table (Message-ID, folder, placed_at) written whenever the scanner, backfill, or sweep moves a message, and **not** cleared by `ClearAllVerdicts`. Add it to the base schema and to `applyOneShotMigrations` guarded by a `schema_meta` key, following the `v2_expiry_redesign_cache_flush` pattern and the `PRAGMA table_info` probe used in Task 13.

`DetectManualClassifications` consults it: a message whose current folder matches a recorded placement was moved by MailReaper, not the user, regardless of whether a verdict row still exists.

- [ ] **Step 4: Run to verify the laundering test passes**

Run: `cd web && go test ./internal/scanner/ -run TestRescanDoesNotLaunderOwnMoves -v`
Expected: PASS. `/api/rescan` keeps its full-clear semantics.

- [ ] **Step 5: Add the cheap refresh endpoint**

`ClearAllVerdicts` + a full no-lookback rescan is expensive: it re-incurs an LLM call for every message. Most of the time the operator wants "re-run the pending queue against current rules and force fresh LLM answers", not a full re-evaluation.

In `verdicts.go`:

```go
// ClearVerdictsByStatus deletes verdicts in the given status and returns the
// number removed.
func (d *DB) ClearVerdictsByStatus(status string) (int64, error) {
	res, err := d.Exec(`DELETE FROM verdicts WHERE status = ?`, status)
	if err != nil {
		return 0, fmt.Errorf("db: clear verdicts by status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: clear verdicts by status: rows affected: %w", err)
	}
	return n, nil
}
```

Add `POST /api/refresh` in `server.go` and a `handleRefresh` in `api.go` that clears `pending` verdicts plus the LLM cache and triggers a normal scan. Leave `handleRescan` as-is.

- [ ] **Step 6: Test both endpoints**

```go
func TestRescanClearsEverything(t *testing.T) {
	// POST /api/rescan -> both the executed and the pending verdict are gone.
}

func TestRefreshPreservesExecutedVerdicts(t *testing.T) {
	// POST /api/refresh -> the executed verdict survives, the pending one is cleared.
}
```

- [ ] **Step 7: Run the suite and commit**

Run: `cd web && go test ./internal/server/ ./internal/scanner/ ./internal/db/ -v`

```bash
git add web/internal/server/ web/internal/db/ web/internal/scanner/
git commit -m "fix: stop rescan laundering its own moves; add a cheap refresh

handleRescan deletes every verdict, and DetectManualClassifications then
found messages in triage folders with no verdict, concluded the user had
filed them, and DistillRules turned that into permanent rules.

Fixing it by preserving executed verdicts would have been worse: ScanAccount
skips any message that already has a verdict, so a rescan could never
re-evaluate what it had already acted on, and the endpoint would stop doing
what its name promises. The laundering is fixed at its source instead --
a durable placements record that survives a verdict clear -- so rescan keeps
its full-clear semantics.

Adds POST /api/refresh for the common case: clear the pending queue and the
LLM cache without re-incurring an LLM call for every message in the mailbox."
```


## Phase 6 — Reliability and UI

### Task 17: Publish the scan_complete event (#10)

**Files:**
- Modify: `web/cmd/mailreaper/main.go:131-185` (`runFullScan`)
- Test: `web/internal/server/sse_test.go` (create if absent)

`web/internal/server/sse.go:55` defines `PublishEvent`, both `dashboard.js` and `review.js` subscribe to `scan_complete`, and there are zero call sites anywhere in the Go code. The live-update contract the UI is built against has never worked, which is also why the Scan Now button falls back to a blind timer (Task 18 depends on this).

- [ ] **Step 1: Confirm the gap**

Run: `cd web && grep -rn "PublishEvent\|\.Publish(" --include="*.go" .`
Expected: only the definitions in `sse.go` and no production call site.

- [ ] **Step 2: Publish at the end of a full scan**

At the end of `runFullScan` in `main.go`, after the account loop:

```go
	server.PublishEvent("scan_complete", map[string]any{
		"accounts":   len(cfg.Accounts),
		"finishedAt": time.Now().UTC().Format(time.RFC3339),
	})
	log.Printf("scan: full scan complete")
```

- [ ] **Step 3: Verify against the running server, not a unit test**

Start the server and watch the real stream:

```bash
cd web && go build -o mailreaper ./cmd/mailreaper && ./mailreaper &
curl -N http://127.0.0.1:8025/api/events &
curl -X POST -H "Content-Type: application/json" http://127.0.0.1:8025/api/scan
```

Expected: the `curl -N` stream emits a `scan_complete` event when the scan finishes. Record the actual output — a passing unit test on the broker alone would not prove the wiring.

- [ ] **Step 4: Commit**

```bash
git add web/cmd/mailreaper/main.go
git commit -m "fix: publish scan_complete so the dashboard updates

The SSE broker, the endpoint, and both client listeners were all in
place, but nothing ever called PublishEvent -- the live-update contract
the UI is written against had never fired once."
```

---

### Task 18: Scan button waits for completion, not a timer (#20)

**Files:**
- Modify: `web/ui/js/dashboard.js:115-129`

Depends on Task 17. The button re-enables after a blind 5-second `setTimeout` — it had to, because the completion event never fired. A real scan takes far longer, so the button invites a second scan while the first is still running, and there is no server-side overlap protection to absorb it.

- [ ] **Step 1: Track scan state and clear it on the real event**

```js
let scanInFlight = false;
let scanFallbackTimer = null;

function setScanInFlight(active) {
  scanInFlight = active;
  const btn = document.getElementById("scan-now-btn");
  if (btn) {
    btn.disabled = active;
    btn.textContent = active ? "Scanning…" : "Scan Now";
  }
}

async function scanNow() {
  if (scanInFlight) return;
  setScanInFlight(true);
  try {
    await api("/api/scan", { method: "POST" });
  } catch (err) {
    setScanInFlight(false);
    showError(err.message);
    return;
  }
  // Safety net only: if scan_complete never arrives (server restart, SSE
  // drop), release the button rather than wedging it forever.
  clearTimeout(scanFallbackTimer);
  scanFallbackTimer = setTimeout(() => setScanInFlight(false), 10 * 60 * 1000);
}
```

In the `scan_complete` handler, call `clearTimeout(scanFallbackTimer); setScanInFlight(false);` before refreshing the dashboard.

- [ ] **Step 2: Verify in the browser**

Start the server, open the dashboard, click Scan Now. Confirm the button stays disabled for the full duration of the scan and re-enables exactly when the log prints `scan: full scan complete`.

- [ ] **Step 3: Commit**

```bash
git add web/ui/js/dashboard.js
git commit -m "fix: Scan Now waits for scan_complete instead of a 5s timer

The button re-enabled on a blind timeout because the completion event
never fired. Real scans take far longer, so it invited a second scan
while the first was still running. Keeps a long fallback so a dropped
SSE connection cannot wedge the button."
```

---

### Task 19: Guard review-queue actions against SSE repaints (#21)

**Files:**
- Modify: `web/ui/js/review.js:60-120`

Buttons are disabled before their request and re-enabled in a `finally`, but an SSE-triggered `renderTable()` swaps in fresh DOM mid-request, resurrecting an enabled button for a verdict whose `PUT` is still in flight. A second click sends a duplicate request; worse, the repaint can reorder rows so the click lands on a different message than the one displayed.

- [ ] **Step 1: Track in-flight IDs outside the DOM**

```js
// DOM state cannot carry the in-flight flag: an SSE repaint replaces the
// buttons mid-request and resurrects them enabled. Track by verdict ID.
const inFlight = new Set();

async function updateVerdict(messageId, status) {
  if (inFlight.has(messageId)) return;
  inFlight.add(messageId);
  try {
    await api(`/api/verdicts/${encodeURIComponent(messageId)}/status`, {
      method: "PUT",
      body: JSON.stringify({ status }),
    });
  } finally {
    inFlight.delete(messageId);
    await loadVerdicts();
  }
}
```

- [ ] **Step 2: Render matching buttons disabled**

In `renderTable()`, emit `disabled` on rows whose ID is in `inFlight`, so a repaint cannot present a clickable button for a pending request:

```js
const busy = inFlight.has(v.messageIdHeader) ? " disabled" : "";
```

Apply to both the approve and reject buttons.

- [ ] **Step 3: Apply the same guard to batch approve**

`batchApprove()` issues N concurrent `PUT`s; route each through `updateVerdict` so the set is respected, and disable the batch button for the duration.

- [ ] **Step 4: Verify in the browser**

With the server running and several pending verdicts, click Approve and immediately trigger a scan so a `scan_complete` repaint lands mid-request. Confirm in the Network tab that exactly one `PUT` is sent per verdict.

- [ ] **Step 5: Commit**

```bash
git add web/ui/js/review.js
git commit -m "fix: SSE repaint no longer resurrects in-flight verdict buttons

Buttons were disabled before their request and re-enabled in a finally,
but an SSE-driven renderTable() replaced the DOM mid-request and brought
back an enabled button for a verdict whose PUT was still in flight.
Tracks in-flight verdict IDs outside the DOM."
```

---

## Phase 7 — Coverage for destructive paths

These three tasks add tests for code that already exists. Each writes tests only; if a test reveals a defect, stop and fix it in its own commit before continuing.

### Task 20: Cover SweepDeferredExpiries (#22)

**Files:**
- Create: `web/internal/scanner/sweep_test.go`

`SweepDeferredExpiries` moves mail and has zero tests at any layer. Depends on Task 8 (without a seeded `expired` category it returns early and every test would pass vacuously).

Signature reference: `(s *Scanner) SweepDeferredExpiries(client MailClient, accountID string) error` — no context parameter. Reuse `openTestDB(t)` and the `mockMailClient` struct literal from `scanner_test.go` (same package, no import needed).

- [ ] **Step 1: Write the happy-path test**

```go
func TestSweepMovesPastDeadlineToExpired(t *testing.T) {
	d := openTestDB(t)
	// Task 8 seeds the "expired" category; assert it resolved rather than
	// letting the sweep's early return make this pass vacuously.
	if s := New(d, cfg).canonicalExpiredFolder(); s == "" {
		t.Fatal("no expired category; this test would pass vacuously")
	}

	past := time.Now().Add(-24 * time.Hour)
	if err := d.SaveVerdict(db.Verdict{
		AccountID:         "acct",
		MessageIDHeader:   "<deferred@test>",
		Subject:           "sale ends yesterday",
		Sender:            "shop@example.test",
		SentAt:            time.Now().Add(-96 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		ExpiresAt:         &past,
		Confidence:        0.9,
		EvaluatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {{
				MessageID: "<deferred@test>",
				Folder:    "Folders/AI-Triage/Promotions",
				UID:       1,
			}},
		},
	}

	s := New(d, cfg)
	if err := s.SweepDeferredExpiries(client, "acct"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}

	if len(client.movedMsgs) != 1 {
		t.Fatalf("recorded %d move(s), want 1", len(client.movedMsgs))
	}
	if client.movedMsgs[0] != "Expired" {
		t.Errorf("moved to %q, want the canonical Expired folder", client.movedMsgs[0])
	}
}
```

- [ ] **Step 2: Write the message-missing test**

Same setup, but the mock's folder listing does not contain the message (the user moved it themselves). Assert no move is attempted and no panic occurs. Document whatever the current behavior is — if the verdict is retried forever, assert that and note it as known.

- [ ] **Step 3: Write the no-category test**

With no `expired` category, assert the sweep returns without moving anything.

- [ ] **Step 4: Run and commit**

Run: `cd web && go test ./internal/scanner/ -run TestSweep -v`

```bash
git add web/internal/scanner/sweep_test.go
git commit -m "test: cover the deferred-expiry sweep

SweepDeferredExpiries moves mail and had no tests at any layer."
```

---

### Task 21: Cover BackfillFolders orchestration (#23)

**Files:**
- Modify: `web/internal/scanner/backfill_test.go`

Only the pure `backfillDestination` helper is covered; the orchestration that actually moves mail is not.

- [ ] **Step 1: Write the move test**

Mirror `TestScanCycleTTLRule`: seed a triage folder with one message and a rule that reclassifies it at confidence >= 0.7. Assert `stats.Moved == 1`, the mock recorded the move, and the saved verdict has `Status: "executed"`.

- [ ] **Step 2: Write the low-confidence test**

Pre-seed a cached verdict with confidence 0.5 so no network call happens. Assert `stats.Skipped == 1`, no move was attempted, and the verdict is `pending`.

- [ ] **Step 3: Run and commit**

Run: `cd web && go test ./internal/scanner/ -run TestBackfill -v`

```bash
git add web/internal/scanner/backfill_test.go
git commit -m "test: cover BackfillFolders orchestration

Only the pure destination helper was covered; the code that actually
moves mail during --backfill was not."
```

---

### Task 22: Cover the auto-execute confidence gate (#24)

**Files:**
- Modify: `web/internal/scanner/scanner_test.go`

The `confidence >= 0.7` gate decides whether a message is moved silently or queued for human review, and its low-confidence and boundary paths are never exercised.

- [ ] **Step 1: Write the low-confidence test**

Set `LLM.Provider` to `"gemini"` and pre-seed a cached verdict at confidence 0.5 so `evaluateLLM` reads the cache instead of the network. Run `ScanAccount` and assert no move occurred and the verdict is `pending`.

- [ ] **Step 2: Write the boundary test**

Same, with confidence exactly 0.7. Assert the message *is* auto-executed, pinning the boundary as inclusive.

- [ ] **Step 3: Run and commit**

Run: `cd web && go test ./internal/scanner/ -v`

```bash
git add web/internal/scanner/scanner_test.go
git commit -m "test: cover the auto-execute confidence gate

The 0.7 threshold decides whether mail moves silently or waits for
review, and neither its reject path nor its boundary was exercised."
```

---

## Finding Coverage

| # | Finding | Task |
|---|---|---|
| 1 | CSRF on state-changing API | 1 |
| 2 | XSS via Message-ID in onclick | 2 |
| 3 | Expires header rule never fires | 4 |
| 4 | Failed move recorded as executed | 9 |
| 5 | Date-only deadlines expire a day early | 5 |
| 6 | Multi-classify never uses the cache | 13 |
| 7 | Backfill does not clear the cache | 10 |
| 8 | Backfill deletes verdict before evaluating | 10 |
| 9 | SQLite pragmas not applied | 12 |
| 10 | scan_complete never published | 17 |
| 11 | LLM cache has no model component | 13 |
| 12 | Category icon unescaped | 2 |
| 13 | Canonical expired category never seeded | 8 |
| 14 | Backfill LLM-outage cascade | 11 |
| 15 | Distill widens to `*@domain` | 15 |
| 16 | Rescan launders auto-moves | 16 |
| 17 | No plausibility floor on deadlines | 7 |
| 18 | TZ unset → UTC in containers | 6 |
| 19 | LAN exposure | 3 |
| 20 | Scan button blind timer | 18 |
| 21 | Review-queue double PUT | 19 |
| 22 | Sweep untested | 20 |
| 23 | Backfill orchestration untested | 21 |
| 24 | Confidence gate untested | 22 |

All 24 findings map to a task. Tasks 2, 10 and 16 each close two concerns, and Task 13 closes two findings (#11 and #6).

**Task numbering note:** there is no Task 14. The original plan split the cache work into Task 13 (model-aware key) and Task 14 (cache multi-classify verdicts). An external review established that the two cannot be separated — both change `llm_cache`, and doing them as two migrations against an unchanged single-column primary key would make the analysis and classify prompt paths clobber and then delete each other's rows. They are merged into Task 13. Numbering of later tasks is unchanged so that task references elsewhere still resolve.

## Review History

This plan was reviewed by OpenAI Codex (`/codex`, high reasoning, read-only) against the actual code before execution. That review found two blockers and six lower-severity problems, all fixed above:

- **Task 13 (blocker):** the original `ALTER TABLE` would fail on fresh databases, because `applyOneShotMigrations` runs *after* the base `CREATE TABLE` block (`db.go:234` vs `db.go:208-222`). Now probes `PRAGMA table_info` first.
- **Task 14 (blocker):** the original design put prompt kind in a value column while leaving the primary key on `message_id_header` alone, so the two prompt paths would overwrite each other — and the model-mismatch-deletes-the-row rule would make them delete each other. Also `CachedVerdict` had no `Category`, so a cached classify hit could not reconstruct its classification. Merged into Task 13 with a real composite key.
- **Task 9:** "save nothing and retry" would re-spend an LLM call every scan on a permanently failing message, and would misread an ambiguous IMAP success as a user filing. Now records `move_failed`.
- **Task 16:** clearing only pending verdicts would have meant rescan could never re-evaluate what it had acted on. Now fixes laundering at its source and adds a separate `/api/refresh`.
- **Tasks 1, 5+7, 6, 8, 12:** overclaimed client compatibility; a dangerous prose reading that would reject legitimate same-day deadlines; a constructor signature change that would have broken every other task's fixture; an INSERT missing NOT NULL columns; and a pragma test that could pass while checking the same connection three times.

## Deployment Note

After Phase 4 lands, flush the LLM cache once so the last old-model entries cannot be replayed by a backfill run that predates Task 13:

```bash
cd web && sqlite3 mailreaper.db "DELETE FROM llm_cache;"
```

Do this with the server stopped. Do **not** use `POST /api/rescan` for this — before Task 16 lands it destroys executed-verdict history for messages already moved outside `folders.scan`, which cannot be rebuilt.
