# Triage-Folder Expiry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mail that a routing rule filed into a perishable triage folder (Promotions, Notifications, Hobbies) gets its content-implied deadline extracted and moves to Expired once that deadline passes.

**Architecture:** A new per-category `check_deadlines` flag marks which folders are perishable. After a routing rule picks a folder, if the verdict carries no deadline and the folder is perishable, one cached LLM call extracts one; a past deadline overrides the folder to Expired, a future one is persisted for the sweep. `SweepDeferredExpiries` is rewritten to do one IMAP fetch per folder instead of one per verdict, and to terminate verdicts whose message has left the folder so its backlog can drain.

**Tech Stack:** Go 1.x, SQLite (`modernc.org/sqlite` via `database/sql`), `go-imap` v2, vanilla ES-module JS for the UI. No test framework beyond the standard `testing` package.

**Spec:** `docs/superpowers/specs/2026-08-31-triage-folder-expiry-design.md`

## Global Constraints

- All work happens in `/Users/kraft/code/mailreaper/web`. Run every command from that directory; the Go module root is there, not at the repo root.
- Run `gofmt -w` on every Go file you touch before committing. The tree is gofmt-clean (commit `09936bb`) and must stay that way.
- Multi-line comment blocks use a single `/* ... */` block. Reserve `//` for genuinely single-line comments. Follow the dominant style of the file you are editing.
- American English everywhere: "behavior", "canonical", "normalize".
- Never add AI credit to commits, code comments, or anything else.
- The standing bias of this codebase is **prefer false negatives over false positives**. When a check is ambiguous, the safe direction is to leave the message where it is, not to expire it.
- The confidence threshold for acting automatically is `0.7`, already expressed as `minExpiryConfidence` in `internal/scanner/scanner.go`. Do not introduce a second threshold constant.
- Existing verdict statuses are `executed`, `pending`, `manual`, `corrected`, `move_failed`. This plan adds exactly one: `orphaned`.
- Do not change prompts, the model, or `extractValidExpiresAt`'s validation logic. Task 4 reuses it verbatim.

---

### Task 1: `check_deadlines` column on categories

**Files:**
- Modify: `web/internal/db/db.go` (categories `CREATE TABLE`; migrations list; new `migrateCategoryCheckDeadlines` method)
- Modify: `web/internal/db/categories.go` (`Category` struct, `GetCategories`, `SaveCategory`)
- Test: `web/internal/db/categories_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `db.Category.CheckDeadlines bool` (JSON field `checkDeadlines`), round-tripped by `GetCategories` and `SaveCategory`. Tasks 2, 4, and 5 all read this field.

- [ ] **Step 1: Write the failing tests**

Append to `web/internal/db/categories_test.go`:

```go
// TestCategoryCheckDeadlinesRoundTrip proves the flag survives a save/load
// cycle in both directions. A one-directional test would pass against a
// SaveCategory whose ON CONFLICT clause forgets the column, which is the
// realistic way to get this wrong.
func TestCategoryCheckDeadlinesRoundTrip(t *testing.T) {
	d := openTestDB(t)

	if err := d.SaveCategory(db.Category{
		ID: "promotions", Name: "Promotions", FolderName: "Promotions",
		CheckDeadlines: true,
	}); err != nil {
		t.Fatalf("SaveCategory: %v", err)
	}
	if err := d.SaveCategory(db.Category{
		ID: "paper-trail", Name: "Paper-Trail", FolderName: "Paper-Trail",
	}); err != nil {
		t.Fatalf("SaveCategory: %v", err)
	}

	got := map[string]bool{}
	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	for _, c := range cats {
		got[c.ID] = c.CheckDeadlines
	}

	if !got["promotions"] {
		t.Error("promotions: CheckDeadlines = false, want true")
	}
	if got["paper-trail"] {
		t.Error("paper-trail: CheckDeadlines = true, want false")
	}
}

// TestCategoryCheckDeadlinesUpsertClearsFlag verifies the flag can be turned
// back off. SaveCategory is an upsert, so a missing column in the ON CONFLICT
// SET list leaves a stale 1 in place forever and the UI checkbox appears
// broken only when unchecking.
func TestCategoryCheckDeadlinesUpsertClearsFlag(t *testing.T) {
	d := openTestDB(t)

	cat := db.Category{ID: "hobbies", Name: "Hobbies", FolderName: "Hobbies", CheckDeadlines: true}
	if err := d.SaveCategory(cat); err != nil {
		t.Fatalf("SaveCategory (on): %v", err)
	}
	cat.CheckDeadlines = false
	if err := d.SaveCategory(cat); err != nil {
		t.Fatalf("SaveCategory (off): %v", err)
	}

	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	for _, c := range cats {
		if c.ID == "hobbies" && c.CheckDeadlines {
			t.Fatal("CheckDeadlines stayed true after saving it false")
		}
	}
}

// TestCheckDeadlinesMigrationSeedsPerishableCategories exercises the upgrade
// path a real user is on: a database whose categories table predates the
// column, holding the plural category ids this deployment actually uses.
//
// The table is created by hand with the old shape, then reopened so
// applyOneShotMigrations runs against it. Asserting on a fresh database
// instead would prove nothing -- the base CREATE TABLE already has the
// column there, and the seeding UPDATE would match no rows.
func TestCheckDeadlinesMigrationSeedsPerishableCategories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE categories (
			id           TEXT PRIMARY KEY,
			name         TEXT,
			folder_name  TEXT,
			icon         TEXT,
			color        TEXT,
			created_at   TIMESTAMP
		)`); err != nil {
		t.Fatalf("create legacy categories table: %v", err)
	}
	for _, row := range [][2]string{
		{"promotions", "Folders/AI-Triage/Promotions"},
		{"notifications", "Folders/AI-Triage/Notifications"},
		{"hobbies", "Folders/AI-Triage/Hobbies"},
		{"newsletters", "Folders/AI-Triage/Newsletters"},
		{"paper-trail", "Folders/AI-Triage/Paper-Trail"},
	} {
		if _, err := legacy.Exec(
			`INSERT INTO categories (id, name, folder_name, created_at) VALUES (?, ?, ?, datetime('now'))`,
			row[0], row[0], row[1],
		); err != nil {
			t.Fatalf("seed legacy category %q: %v", row[0], err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open on legacy database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	got := map[string]bool{}
	for _, c := range cats {
		got[c.ID] = c.CheckDeadlines
	}

	for _, id := range []string{"promotions", "notifications", "hobbies"} {
		if !got[id] {
			t.Errorf("%s: CheckDeadlines = false after migration, want true", id)
		}
	}
	for _, id := range []string{"newsletters", "paper-trail", "expired"} {
		if got[id] {
			t.Errorf("%s: CheckDeadlines = true after migration, want false", id)
		}
	}
}
```

Add the imports this needs to the top of the file:

```go
import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/kraftbj/mailreaper/internal/db"

	_ "modernc.org/sqlite"
)
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/db/ -run TestCategoryCheckDeadlines -v
```

Expected: compile failure — `unknown field CheckDeadlines in struct literal of type db.Category`.

- [ ] **Step 3: Add the column to the base schema**

In `web/internal/db/db.go`, find the `CREATE TABLE IF NOT EXISTS categories` statement and add the column as the last one:

```go
		`CREATE TABLE IF NOT EXISTS categories (
			id              TEXT PRIMARY KEY,
			name            TEXT,
			folder_name     TEXT,
			icon            TEXT,
			color           TEXT,
			created_at      TIMESTAMP,
			check_deadlines INTEGER NOT NULL DEFAULT 0
		)`,
```

- [ ] **Step 4: Add the one-shot migration**

In `web/internal/db/db.go`, append this entry to the `migrations` slice in `applyOneShotMigrations`, immediately after the `v6_placements_backfill` entry:

```go
		/* 2026-08-31: perishable categories. Mail routed into Promotions,
		Notifications, or Hobbies by a pattern-matching classify rule never
		had its deadline extracted, because EvaluateClassify short-circuits
		the only rules that ask. The scanner now runs an extraction pass
		after routing, gated on this flag so Paper-Trail and Newsletters --
		which do not expire -- cost nothing. See the design doc:
		docs/superpowers/specs/2026-08-31-triage-folder-expiry-design.md */
		{key: "v7_category_check_deadlines", fn: (*DB).migrateCategoryCheckDeadlines},
```

Then add the method itself, next to `migratePlacements` at the bottom of the file:

```go
/*
migrateCategoryCheckDeadlines adds categories.check_deadlines to databases
created before the column existed, then flags the perishable built-in
categories.

Fresh databases already have the column from the base schema's CREATE TABLE
(applyOneShotMigrations runs after that block), so the ALTER is guarded by a
PRAGMA probe the same way migrateLLMCacheCompositeKey guards its rebuild.
The seeding UPDATE runs either way: on a fresh database only the "expired"
category exists, so it matches nothing.

Both singular and plural ids are listed. The llm-classify rules in
rules/defaults.go name categories in the singular ("promotion",
"notification"), while the category rows this deployment actually holds are
plural ("promotions", "notifications"). Matching one form only would leave
the other unflagged, and an unflagged category expires nothing -- a failure
invisible until mail has piled up for months, which is exactly how the
stalled sweep went unnoticed.

The default stays 0. A category the user adds later and never thinks about
must not silently start expiring mail.
*/
func (d *DB) migrateCategoryCheckDeadlines() error {
	var hasColumn bool
	rows, err := d.Query(`PRAGMA table_info(categories)`)
	if err != nil {
		return fmt.Errorf("probe categories schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan categories schema: %w", err)
		}
		if name == "check_deadlines" {
			hasColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate categories schema: %w", err)
	}

	if !hasColumn {
		if _, err := d.Exec(`ALTER TABLE categories ADD COLUMN check_deadlines INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add categories.check_deadlines: %w", err)
		}
	}

	if _, err := d.Exec(`
		UPDATE categories SET check_deadlines = 1
		WHERE id IN ('promotion', 'promotions', 'notification', 'notifications', 'hobby', 'hobbies')
	`); err != nil {
		return fmt.Errorf("seed categories.check_deadlines: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Carry the field through `categories.go`**

In `web/internal/db/categories.go`, add the struct field:

```go
type Category struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	FolderName string    `json:"folderName"`
	Icon       string    `json:"icon"`
	Color      string    `json:"color"`
	// CheckDeadlines marks the category as perishable: mail routed into its
	// folder gets a deadline-extraction pass even when the routing rule
	// supplied no deadline. Off by default -- see
	// migrateCategoryCheckDeadlines for why.
	CheckDeadlines bool      `json:"checkDeadlines"`
	CreatedAt      time.Time `json:"createdAt"`
}
```

Replace `GetCategories` with:

```go
func (d *DB) GetCategories() ([]Category, error) {
	rows, err := d.Query(`
		SELECT id, name, folder_name, icon, color, created_at,
		       COALESCE(check_deadlines, 0)
		FROM categories
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: get categories: %w", err)
	}
	defer rows.Close()

	var cats []Category
	for rows.Next() {
		var c Category
		var createdAtStr string
		/* Scanned as an int rather than straight into the bool field:
		SQLite has no boolean type, and relying on the driver's int64->bool
		conversion is a portability bet with nothing to gain. */
		var checkDeadlines int
		if err := rows.Scan(&c.ID, &c.Name, &c.FolderName, &c.Icon, &c.Color, &createdAtStr, &checkDeadlines); err != nil {
			return nil, fmt.Errorf("db: scan category: %w", err)
		}
		c.CheckDeadlines = checkDeadlines != 0
		if c.CreatedAt, err = parseDBTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("db: parse category created_at: %w", err)
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}
```

Replace `SaveCategory` with:

```go
func (d *DB) SaveCategory(c Category) error {
	now := time.Now().UTC()
	checkDeadlines := 0
	if c.CheckDeadlines {
		checkDeadlines = 1
	}
	_, err := d.Exec(`
		INSERT INTO categories (id, name, folder_name, icon, color, created_at, check_deadlines)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name            = excluded.name,
			folder_name     = excluded.folder_name,
			icon            = excluded.icon,
			color           = excluded.color,
			check_deadlines = excluded.check_deadlines
	`, c.ID, c.Name, c.FolderName, c.Icon, c.Color, formatDBTime(now), checkDeadlines)
	if err != nil {
		return fmt.Errorf("db: save category: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
cd /Users/kraft/code/mailreaper/web && gofmt -w internal/db/ && go test ./internal/db/ -v
```

Expected: PASS, including the three new tests and every pre-existing one.

- [ ] **Step 7: Commit**

```bash
cd /Users/kraft/code/mailreaper/web
git add internal/db/db.go internal/db/categories.go internal/db/categories_test.go
git commit -m "feat: mark categories as perishable with check_deadlines

Mail routed into a triage folder by a pattern-matching classify rule
never gets a deadline extracted, because EvaluateClassify short-circuits
the rules that ask. The scanner will soon run an extraction pass after
routing; this flag is what keeps that pass off Paper-Trail and
Newsletters, which never expire and account for well over half the
classify-routed volume.

Seeds the plural ids this deployment uses alongside the singular ones
defaults.go names, since an unflagged category expires nothing and the
mismatch would be silent."
```

---

### Task 2: Expose the flag through the API and settings UI

**Files:**
- Modify: `web/ui/settings.html` (category modal, categories table header)
- Modify: `web/ui/js/settings.js` (`renderCategories`, `openCategoryModal`, `saveCategory`)
- Test: `web/internal/server/api_test.go`

**Interfaces:**
- Consumes: `db.Category.CheckDeadlines` from Task 1.
- Produces: nothing later tasks depend on. This task is user-facing only.

`handleSaveCategory` decodes straight into `db.Category` and `handleGetCategories` encodes it, so no Go changes are needed in `internal/server` — but that is exactly the kind of claim worth a test, because a JSON tag typo breaks it silently.

- [ ] **Step 1: Write the failing test**

Append to `web/internal/server/api_test.go`:

```go
// TestCategoryCheckDeadlinesSurvivesAPIRoundTrip guards the JSON contract
// between the settings page and the database. handleSaveCategory decodes
// straight into db.Category, so a mistyped or missing tag on CheckDeadlines
// costs no compile error and no runtime error -- the checkbox simply never
// takes effect, and the perishable-category gate silently matches nothing.
func TestCategoryCheckDeadlinesSurvivesAPIRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)

	body := bytes.NewBufferString(`{
		"id": "promotions",
		"name": "Promotions",
		"folderName": "Folders/AI-Triage/Promotions",
		"checkDeadlines": true
	}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, newRequest(http.MethodPost, "/api/categories", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/categories: status %d, body %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, newRequest(http.MethodGet, "/api/categories", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/categories: status %d", rec.Code)
	}

	var cats []db.Category
	if err := json.Unmarshal(rec.Body.Bytes(), &cats); err != nil {
		t.Fatalf("decode categories: %v", err)
	}
	for _, c := range cats {
		if c.ID == "promotions" {
			if !c.CheckDeadlines {
				t.Fatal("checkDeadlines did not survive the API round trip")
			}
			return
		}
	}
	t.Fatal("saved category was not returned by GET /api/categories")
}
```

`*server.Server` implements `http.Handler` directly (`func (s *Server) ServeHTTP`), which is how the other tests in this file drive it — there is no separate accessor.

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /Users/kraft/code/mailreaper/web && go test ./internal/server/ -run TestCategoryCheckDeadlinesSurvivesAPIRoundTrip -v
```

Expected: FAIL — `checkDeadlines did not survive the API round trip`, unless Task 1's JSON tag is already correct, in which case it passes immediately. A passing result here is an acceptable outcome for this step: the test's value is as a regression guard, and you have just confirmed the contract holds. Note it and move on.

- [ ] **Step 3: Add the checkbox to the modal**

In `web/ui/settings.html`, inside `<div id="category-modal">`, add a form group after the Color group and before `<div class="modal-footer">`:

```html
    <div class="form-group">
      <label class="form-label">
        <input type="checkbox" id="cat-check-deadlines">
        Check for deadlines
      </label>
      <div class="text-muted" style="font-size:12px;margin-top:4px;">
        Mail filed here is scanned for a deadline in its content ("sale ends
        Friday") and moved to Expired once that date passes. Leave off for
        categories that never go stale, such as receipts and newsletters.
      </div>
    </div>
```

In the same file, add a header cell to the categories table so the new column in Step 4 lines up. Find the `<thead>` row above `<tbody id="categories-tbody">` and add `<th>Deadlines</th>` immediately before the actions column header.

- [ ] **Step 4: Wire it up in `settings.js`**

In `web/ui/js/settings.js`, in `renderCategories`, add a cell after the color cell:

```js
      <td>${c.checkDeadlines ? "Yes" : "—"}</td>
```

and change the empty-state `colspan` from `5` to `6`:

```js
    tbody.innerHTML = `<tr><td colspan="6" class="empty">No categories yet.</td></tr>`;
```

In `openCategoryModal`, add:

```js
  document.getElementById("cat-check-deadlines").checked = Boolean(cat?.checkDeadlines);
```

In `saveCategory`, add the field to the `cat` object:

```js
  const cat = {
    id,
    name,
    folderName: document.getElementById("cat-folder").value.trim() || name,
    icon: document.getElementById("cat-icon").value.trim(),
    color: document.getElementById("cat-color").value.trim(),
    checkDeadlines: document.getElementById("cat-check-deadlines").checked,
  };
```

- [ ] **Step 5: Run the tests**

```bash
cd /Users/kraft/code/mailreaper/web && go test ./... 
```

Expected: PASS.

- [ ] **Step 6: Verify in the browser**

Start the server, open Settings, and confirm: the Deadlines column shows "Yes" for Promotions / Notifications / Hobbies and "—" for Newsletters / Paper-Trail; editing a category shows the checkbox in the right state; toggling it off and reopening shows it off. Do not skip this — Steps 3 and 4 have no automated coverage, and an id typo produces a silently inert checkbox.

- [ ] **Step 7: Commit**

```bash
cd /Users/kraft/code/mailreaper/web
git add ui/settings.html ui/js/settings.js internal/server/api_test.go
git commit -m "feat: expose the perishable-category flag in Settings

Adds the checkbox and a table column, plus an API round-trip test. The
server needed no change -- handleSaveCategory decodes straight into
db.Category -- which is precisely why the contract is worth a test: a
mistyped JSON tag would leave the checkbox inert with no error anywhere."
```

---

### Task 3: Rewrite `SweepDeferredExpiries` to be bounded and self-draining

**Files:**
- Modify: `web/internal/db/verdicts.go` (`GetDeferredExpiries` gains an account filter and excludes the new status)
- Modify: `web/internal/scanner/sweep.go` (full rewrite of the loop)
- Modify: `web/internal/scanner/scanner_test.go` (mock gains a per-folder call counter)
- Test: `web/internal/scanner/sweep_test.go`

**Interfaces:**
- Consumes: nothing from Tasks 1 and 2.
- Produces: `db.GetDeferredExpiries(accountID string) ([]Verdict, error)` — signature change, one non-test caller (`internal/scanner/sweep.go`). `scanner.orphanedStatus = "orphaned"`. `mockMailClient.folderFetches map[string]int`, used by Tasks 3 and 5.

This task stands alone deliberately. The spec's rollout lands it before any new extraction spend, so that draining the live 191-row backlog confirms the diagnosis on its own.

- [ ] **Step 1: Add a per-folder fetch counter to the mock**

In `web/internal/scanner/scanner_test.go`, add the field to `mockMailClient` and count in `GetMessagesInFolder`:

```go
type mockMailClient struct {
	messages       []imappkg.FetchedMessage
	movedMsgs      []string
	inboxMsgIDs    []string
	folderMessages map[string][]imappkg.FetchedMessage
	moveErr        error // when set, MoveMessage fails without recording
	fetchBodyCalls int   // counts FetchBody invocations; a proxy for "was content/LLM evaluation attempted"
	// folderFetches counts GetMessagesInFolder calls per folder. The sweep's
	// cost is the point of its rewrite, so it has to be observable: the
	// pre-rewrite version fetched once per verdict, not once per folder.
	folderFetches map[string]int
}

func (m *mockMailClient) GetMessagesInFolder(folder string) ([]imappkg.FetchedMessage, error) {
	if m.folderFetches == nil {
		m.folderFetches = map[string]int{}
	}
	m.folderFetches[folder]++
	return m.folderMessages[folder], nil
}
```

- [ ] **Step 2: Write the failing tests**

In `web/internal/scanner/sweep_test.go`, **replace** the existing `TestSweepMessageMissingFromFolder` — it documents the pre-rewrite behavior and is now wrong — with the two tests below, and append the third.

```go
// TestSweepMessageMissingFromFolder verifies that a deferred verdict whose
// message is no longer in the folder it recorded is terminated rather than
// retried forever.
//
// This replaces a test that asserted the opposite: it documented "the same
// verdict is picked up again on the next sweep" as current behavior. That
// behavior is the reason the live database accumulated 191 past-due verdicts
// stuck for as long as four months -- every one of them re-listed its folder
// on every scan cycle and could never be satisfied.
func TestSweepMessageMissingFromFolder(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()
	s := New(d, cfg)

	expiredFolder := s.canonicalExpiredFolder()
	if expiredFolder == "" {
		t.Fatal("no expired category resolved; this test would pass vacuously")
	}
	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	if err := d.SaveVerdict(db.Verdict{
		AccountID:         "acct",
		MessageIDHeader:   "<gone@test>",
		Subject:           "user moved this themselves",
		SentAt:            time.Now().Add(-96 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		ExpiresAt:         &past,
		Confidence:        0.9,
		EvaluatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	// The folder exists but does not contain the message.
	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {{MessageID: "<someone-else@test>", UID: 9}},
		},
	}

	if err := s.SweepDeferredExpiries(client, "acct"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}
	if len(client.movedMsgs) != 0 {
		t.Errorf("recorded %d move(s), want 0", len(client.movedMsgs))
	}

	v, err := d.GetVerdictByMessageID("<gone@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected the verdict to survive as a row, got nil")
	}
	if v.Status != orphanedStatus {
		t.Errorf("Status = %q, want %q", v.Status, orphanedStatus)
	}
	if v.ExpiresAt == nil {
		t.Error("ExpiresAt was cleared; it should be preserved for forensics")
	}

	// The whole point: it must not come back.
	again, err := d.GetDeferredExpiries("acct")
	if err != nil {
		t.Fatalf("GetDeferredExpiries: %v", err)
	}
	for _, e := range again {
		if e.MessageIDHeader == "<gone@test>" {
			t.Fatal("orphaned verdict is still returned by GetDeferredExpiries; the backlog cannot drain")
		}
	}
}

// TestSweepFetchesEachFolderOnce is the regression test for the defect that
// stalled the live sweep: GetMessagesInFolder was called inside the
// per-verdict loop, so N past-due verdicts meant N full SELECT + SEARCH ALL +
// FETCH ENVELOPE round trips against folders holding thousands of messages.
//
// Six verdicts across two folders must cost exactly two fetches.
func TestSweepFetchesEachFolderOnce(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()
	s := New(d, cfg)

	expiredFolder := s.canonicalExpiredFolder()
	if expiredFolder == "" {
		t.Fatal("no expired category resolved; this test would pass vacuously")
	}
	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	folders := []string{"Folders/AI-Triage/Promotions", "Folders/AI-Triage/Hobbies"}
	folderMessages := map[string][]imappkg.FetchedMessage{}

	uid := uint32(1)
	for _, folder := range folders {
		for i := 0; i < 3; i++ {
			id := fmt.Sprintf("<msg-%d@test>", uid)
			if err := d.SaveVerdict(db.Verdict{
				AccountID:         "acct",
				MessageIDHeader:   id,
				Subject:           id,
				SentAt:            time.Now().Add(-96 * time.Hour),
				Status:            "executed",
				DestinationFolder: folder,
				ExpiresAt:         &past,
				Confidence:        0.9,
				EvaluatedAt:       time.Now().UTC(),
			}); err != nil {
				t.Fatalf("SaveVerdict: %v", err)
			}
			folderMessages[folder] = append(folderMessages[folder], imappkg.FetchedMessage{
				MessageID: id, Folder: folder, UID: uid,
			})
			uid++
		}
	}

	client := &mockMailClient{folderMessages: folderMessages}
	if err := s.SweepDeferredExpiries(client, "acct"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}

	if len(client.movedMsgs) != 6 {
		t.Errorf("moved %d message(s), want 6", len(client.movedMsgs))
	}
	for _, folder := range folders {
		if got := client.folderFetches[folder]; got != 1 {
			t.Errorf("GetMessagesInFolder(%q) called %d time(s), want exactly 1", folder, got)
		}
	}
}

// TestSweepIgnoresOtherAccounts verifies the sweep is scoped to the account
// whose IMAP client is connected. GetDeferredExpiries had no account filter,
// so in a multi-account setup every account's expiries were swept against
// whichever client happened to be connected. That was merely wasteful before;
// combined with orphaning it is destructive, because the other account's
// messages are guaranteed not to be found.
func TestSweepIgnoresOtherAccounts(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()
	s := New(d, cfg)

	if s.canonicalExpiredFolder() == "" {
		t.Fatal("no expired category resolved; this test would pass vacuously")
	}
	if err := d.UpsertAccount("acct-a", "Account A"); err != nil {
		t.Fatalf("upsert account a: %v", err)
	}
	if err := d.UpsertAccount("acct-b", "Account B"); err != nil {
		t.Fatalf("upsert account b: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	if err := d.SaveVerdict(db.Verdict{
		AccountID:         "acct-b",
		MessageIDHeader:   "<other-account@test>",
		Subject:           "belongs to the other account",
		SentAt:            time.Now().Add(-96 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		ExpiresAt:         &past,
		Confidence:        0.9,
		EvaluatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	client := &mockMailClient{folderMessages: map[string][]imappkg.FetchedMessage{}}
	if err := s.SweepDeferredExpiries(client, "acct-a"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}

	if len(client.folderFetches) != 0 {
		t.Errorf("fetched %d folder(s) for an account with no expiries, want 0", len(client.folderFetches))
	}
	v, err := d.GetVerdictByMessageID("<other-account@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v.Status != "executed" {
		t.Errorf("Status = %q, want executed; the other account's verdict must not be touched", v.Status)
	}
}
```

Add `"fmt"` to the test file's imports.

- [ ] **Step 3: Run the tests to verify they fail**

```bash
cd /Users/kraft/code/mailreaper/web && go test ./internal/scanner/ -run 'TestSweep' -v
```

Expected: compile failure — `undefined: orphanedStatus`, and `GetDeferredExpiries` called with one argument when it takes none.

- [ ] **Step 4: Scope `GetDeferredExpiries` to an account**

In `web/internal/db/verdicts.go`, replace the query and signature:

```go
/*
GetDeferredExpiries returns verdicts for one account that have an expires_at
in the past but have not been moved to the expired folder yet -- messages
classified with a future expiry date that has now arrived.

The account filter is load-bearing rather than cosmetic. SweepDeferredExpiries
runs once per connected account; without it, every account's expiries were
swept against whichever account's IMAP client happened to be connected,
guaranteeing not-found results for the others.

The status filter also excludes 'orphaned' by construction: those are
verdicts whose message has left the folder they recorded, which no sweep can
ever satisfy. See SweepDeferredExpiries.
*/
func (d *DB) GetDeferredExpiries(accountID string) ([]Verdict, error) {
	rows, err := d.Query(`
		SELECT id, account_id, message_id_header, subject, sender, sent_at,
		       rule_id, status, destination_folder, expires_at, reason,
		       confidence, evaluated_at, acted_at
		FROM verdicts
		WHERE expires_at IS NOT NULL
		  AND expires_at != ''
		  AND account_id = ?
		  AND status IN ('executed', 'manual')
		  AND destination_folder NOT LIKE '%Expired%'
		ORDER BY expires_at ASC
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("db: get deferred expiries: %w", err)
	}
	defer rows.Close()

	all, err := scanVerdicts(rows)
	if err != nil {
		return nil, err
	}

	// Filter to only those whose expires_at is in the past.
	now := time.Now()
	var expired []Verdict
	for _, v := range all {
		if v.ExpiresAt != nil && now.After(*v.ExpiresAt) {
			expired = append(expired, v)
		}
	}
	return expired, nil
}
```

Then check for other callers and update them:

```bash
cd /Users/kraft/code/mailreaper/web && grep -rn "GetDeferredExpiries" --include=*.go .
```

- [ ] **Step 5: Rewrite the sweep**

Replace the body of `SweepDeferredExpiries` in `web/internal/scanner/sweep.go`:

```go
/*
orphanedStatus marks a deferred-expiry verdict whose message is no longer in
the folder the verdict recorded -- the user moved it, or deleted it. Such a
verdict can never be satisfied by a move, and GetDeferredExpiries filters on
status IN ('executed','manual'), so setting this drops the row out of the
sweep's result set permanently.

Leaving them in place is what let the live backlog grow to 191 rows, the
oldest four months past its deadline: each one re-listed its folder on every
scan cycle, found nothing, and stayed.
*/
const orphanedStatus = "orphaned"

/*
SweepDeferredExpiries moves messages whose recorded expires_at has now passed
into the canonical Expired folder.

Expiries are grouped by folder before any IMAP work happens, so the cost is
one SELECT + SEARCH + FETCH per distinct folder rather than one per verdict.
The previous version called GetMessagesInFolder inside the per-verdict loop
and re-fetched the same folder dozens of times in a single sweep against
folders holding thousands of messages.
*/
func (s *Scanner) SweepDeferredExpiries(client MailClient, accountID string) error {
	expiries, err := s.db.GetDeferredExpiries(accountID)
	if err != nil {
		return fmt.Errorf("sweep deferred: get expiries: %w", err)
	}
	if len(expiries) == 0 {
		return nil
	}

	expiredFolder := s.canonicalExpiredFolder()
	if expiredFolder == "" {
		return nil
	}

	byFolder := map[string][]db.Verdict{}
	for _, v := range expiries {
		if v.DestinationFolder == "" || v.DestinationFolder == expiredFolder {
			continue
		}
		byFolder[v.DestinationFolder] = append(byFolder[v.DestinationFolder], v)
	}
	if len(byFolder) == 0 {
		return nil
	}

	if err := client.EnsureFolder(expiredFolder); err != nil {
		return fmt.Errorf("sweep deferred: ensure folder %q: %w", expiredFolder, err)
	}

	swept, orphaned := 0, 0
	for folder, verdicts := range byFolder {
		msgs, err := client.GetMessagesInFolder(folder)
		if err != nil {
			log.Printf("sweep deferred: get messages in %q: %v", folder, err)
			continue
		}

		uidByMessageID := make(map[string]uint32, len(msgs))
		for _, m := range msgs {
			if m.MessageID != "" {
				uidByMessageID[m.MessageID] = m.UID
			}
		}

		for _, v := range verdicts {
			uid, present := uidByMessageID[v.MessageIDHeader]
			if !present {
				v.Status = orphanedStatus
				if err := s.db.SaveVerdict(v); err != nil {
					log.Printf("sweep deferred: mark %q orphaned: %v", v.MessageIDHeader, err)
					continue
				}
				orphaned++
				log.Printf("sweep deferred: %q is no longer in %q; marked orphaned", v.MessageIDHeader, folder)
				continue
			}

			if err := client.MoveMessage(folder, uid, expiredFolder); err != nil {
				log.Printf("sweep deferred: move %q: %v", v.MessageIDHeader, err)
				continue
			}

			v.DestinationFolder = expiredFolder
			v.Status = "executed"
			now := time.Now().UTC()
			v.ActedAt = &now
			if err := s.db.SaveVerdict(v); err != nil {
				log.Printf("sweep deferred: update verdict %q: %v", v.MessageIDHeader, err)
			}

			if err := s.db.RecordPlacement(v.MessageIDHeader, expiredFolder); err != nil {
				log.Printf("sweep deferred: record placement for %q: %v", v.MessageIDHeader, err)
			}

			reason := "Deferred expiry"
			if v.ExpiresAt != nil {
				reason = fmt.Sprintf("Expired at %s", v.ExpiresAt.Format("2006-01-02"))
			}
			if err := s.db.LogActivity(db.ActivityEntry{
				Type:            "expired",
				AccountID:       accountID,
				MessageIDHeader: v.MessageIDHeader,
				Subject:         v.Subject,
				Sender:          v.Sender,
				RuleName:        "Deferred expiry",
				Destination:     expiredFolder,
				Reason:          reason,
				Confidence:      1.0,
			}); err != nil {
				log.Printf("sweep deferred: log activity: %v", err)
			}

			swept++
		}
	}

	if swept > 0 || orphaned > 0 {
		log.Printf("sweep: moved %d deferred-expiry message(s) to %q, orphaned %d whose message had left its folder",
			swept, expiredFolder, orphaned)
	}
	return nil
}
```

Note the `v.ExpiresAt != nil` guard on the reason string: the previous version dereferenced it unconditionally, which is safe only because `GetDeferredExpiries` filters nils — a coupling worth not relying on.

- [ ] **Step 6: Run the tests to verify they pass**

```bash
cd /Users/kraft/code/mailreaper/web && gofmt -w internal/db/ internal/scanner/ && go test ./... -v 2>&1 | tail -40
```

Expected: PASS across the module, including every pre-existing scanner test.

- [ ] **Step 7: Commit**

```bash
cd /Users/kraft/code/mailreaper/web
git add internal/db/verdicts.go internal/scanner/sweep.go internal/scanner/sweep_test.go internal/scanner/scanner_test.go
git commit -m "fix: the deferred-expiry sweep could not drain its backlog

Two defects, both visible in the live database as 191 past-due verdicts
still sitting in triage folders, the oldest four months stale.

GetMessagesInFolder was called once per verdict rather than once per
folder, so the sweep re-listed the same multi-thousand-message folder
dozens of times per cycle. And a verdict whose message had left the
recorded folder was left untouched, so it came back on every subsequent
sweep and could never be satisfied -- the backlog could only grow. Such
verdicts are now marked orphaned, which drops them from the query.

Also scopes GetDeferredExpiries to an account. It had no filter, so a
multi-account setup swept every account's expiries against whichever
client was connected; harmless before, destructive alongside orphaning."
```

---

### Task 4: Extract a deadline after routing

**Files:**
- Create: `web/internal/scanner/deadline.go`
- Modify: `web/internal/scanner/scanner.go` (`ScanAccount` auto-execute branch; `evaluateLLM` refactored onto the shared helper)
- Test: `web/internal/scanner/deadline_test.go`

**Interfaces:**
- Consumes: `db.Category.CheckDeadlines` (Task 1). Does not depend on Task 3, but the spec's rollout lands Task 3 first.
- Produces:
  - `func (s *Scanner) deadlineCheckedFolders() map[string]bool` — keys are **lowercased** folder names.
  - `func (s *Scanner) analyzeDeadline(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, customPrompt, category string) (*llm.LLMResponse, error)`
  - `func (s *Scanner) applyDeadlineCheck(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, verdict *rules.RuleVerdict, dest string, checked map[string]bool, expiredFolder string) (string, *time.Time, string)` — returns `(destination, expiresAt, reason)`. Task 5 calls this.

- [ ] **Step 1: Write the failing tests**

Create `web/internal/scanner/deadline_test.go`:

```go
package scanner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/rules"
)

/*
deadlineTestScanner wires a Scanner to a local httptest.Server standing in
for an Ollama model, using the same fake-response shape as the backfill LLM
tests (see fakeOllamaResponse in backfill_test.go). It returns the scanner
and a pointer to the call counter, so a test can assert not only what the
extraction decided but whether it spent a call at all -- the cost argument
is the entire reason the check_deadlines gate exists.
*/
func deadlineTestScanner(t *testing.T, d *db.DB, expiresAt, reason string, confidence float64) (*Scanner, *int32) {
	t.Helper()

	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		content, err := json.Marshal(struct {
			ExpiresAt  string  `json:"expiresAt"`
			Reason     string  `json:"reason"`
			Confidence float64 `json:"confidence"`
		}{ExpiresAt: expiresAt, Reason: reason, Confidence: confidence})
		if err != nil {
			t.Errorf("marshal fake LLM content: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(fakeOllamaResponse{
			Message: fakeOllamaMessage{Role: "assistant", Content: string(content)},
		}); err != nil {
			t.Errorf("encode fake ollama response: %v", err)
		}
	}))
	t.Cleanup(ts.Close)

	cfg := testConfig()
	cfg.LLM = config.LLMConfig{
		Provider: "ollama",
		Ollama:   config.OllamaConfig{Endpoint: ts.URL, Model: "test-model"},
	}
	return New(d, cfg), &calls
}

// seedDeadlineCategories creates one perishable and one non-perishable
// category, mirroring the live shape (plural ids, nested folder names).
func seedDeadlineCategories(t *testing.T, d *db.DB) {
	t.Helper()
	if err := d.SaveCategory(db.Category{
		ID: "promotions", Name: "Promotions",
		FolderName: "Folders/AI-Triage/Promotions", CheckDeadlines: true,
	}); err != nil {
		t.Fatalf("save promotions category: %v", err)
	}
	if err := d.SaveCategory(db.Category{
		ID: "newsletters", Name: "Newsletters",
		FolderName: "Folders/AI-Triage/Newsletters", CheckDeadlines: false,
	}); err != nil {
		t.Fatalf("save newsletters category: %v", err)
	}
}

func deadlineTestMessage() *imappkg.FetchedMessage {
	return &imappkg.FetchedMessage{
		UID:       1,
		MessageID: "<promo@test>",
		Subject:   "Sale ends Friday",
		Sender:    "shop@example.test",
		Date:      time.Now().Add(-96 * time.Hour),
		Folder:    "INBOX",
	}
}

// TestDeadlineCheckedFoldersUsesLowercaseKeys pins the key convention every
// caller depends on. A map keyed by the raw folder name would silently miss
// on any case difference between the category row and the rule's
// destination, and a missed lookup reads as "not perishable" -- a false
// negative that is invisible.
func TestDeadlineCheckedFoldersUsesLowercaseKeys(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)
	s := New(d, testConfig())

	checked := s.deadlineCheckedFolders()
	if !checked["folders/ai-triage/promotions"] {
		t.Error("perishable folder missing from the set under its lowercased name")
	}
	if checked["folders/ai-triage/newsletters"] {
		t.Error("non-perishable folder is in the set")
	}
}

// TestApplyDeadlineCheckPastDeadlineRoutesToExpired is the user-facing
// behavior this whole change exists for: a promo email filed into a
// perishable folder, whose body says the sale is over, must end up in
// Expired instead of the folder the routing rule chose.
func TestApplyDeadlineCheckPastDeadlineRoutesToExpired(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	past := time.Now().Add(-24 * time.Hour) // after the send date, before now
	s, calls := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended", 0.95)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0, Reason: "pattern rule"}
	dest, expiresAt, reason := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Promotions",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/AI-Triage/Expired" {
		t.Errorf("dest = %q, want the canonical Expired folder", dest)
	}
	if expiresAt == nil {
		t.Fatal("expiresAt = nil, want the extracted deadline")
	}
	if reason != "sale ended" {
		t.Errorf("reason = %q, want the model's reason", reason)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Errorf("made %d LLM call(s), want 1", atomic.LoadInt32(calls))
	}
}

// TestApplyDeadlineCheckFutureDeadlineKeepsFolder verifies the deferred case:
// the message stays where the routing rule put it, but carries a deadline so
// SweepDeferredExpiries can act on the day.
func TestApplyDeadlineCheckFutureDeadlineKeepsFolder(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	future := time.Now().Add(72 * time.Hour)
	s, _ := deadlineTestScanner(t, d, future.Format(time.RFC3339), "sale ends Friday", 0.95)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0, Reason: "pattern rule"}
	dest, expiresAt, _ := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Promotions",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/AI-Triage/Promotions" {
		t.Errorf("dest = %q, want the routing rule's folder", dest)
	}
	if expiresAt == nil {
		t.Fatal("expiresAt = nil; the deadline must be persisted for the sweep")
	}
	if !expiresAt.After(time.Now()) {
		t.Error("expiresAt is not in the future")
	}
}

// TestApplyDeadlineCheckSkipsNonPerishableFolder asserts on the call count,
// not just the return value. Returning the folder unchanged while still
// having spent a call would pass a weaker test and defeat the only reason
// the flag exists: Newsletters alone is over half the classify-routed volume.
func TestApplyDeadlineCheckSkipsNonPerishableFolder(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	past := time.Now().Add(-24 * time.Hour)
	s, calls := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended", 0.95)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0}
	dest, expiresAt, _ := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Newsletters",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/AI-Triage/Newsletters" {
		t.Errorf("dest = %q, want the folder unchanged", dest)
	}
	if expiresAt != nil {
		t.Error("expiresAt was set for a non-perishable folder")
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("made %d LLM call(s) for a non-perishable folder, want 0", got)
	}
}

// TestApplyDeadlineCheckSkipsVerdictThatAlreadyHasOne guards against paying
// twice: llm-classify verdicts already carry a deadline from their combined
// call, and re-asking would double the spend on the largest population.
func TestApplyDeadlineCheckSkipsVerdictThatAlreadyHasOne(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	other := time.Now().Add(48 * time.Hour)
	s, calls := deadlineTestScanner(t, d, other.Format(time.RFC3339), "from the model", 0.95)

	existing := time.Now().Add(24 * time.Hour)
	verdict := &rules.RuleVerdict{
		Classified: true, Confidence: 1.0,
		ExpiresAt: &existing, Reason: "from the classify call",
	}
	dest, expiresAt, reason := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Promotions",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("made %d LLM call(s) for a verdict that already had a deadline, want 0", got)
	}
	if dest != "Folders/AI-Triage/Promotions" {
		t.Errorf("dest = %q, want unchanged", dest)
	}
	if expiresAt == nil || !expiresAt.Equal(existing) {
		t.Error("the existing deadline was not preserved")
	}
	if reason != "from the classify call" {
		t.Errorf("reason = %q, want the original verdict's reason", reason)
	}
}

// TestApplyDeadlineCheckDropsLowConfidence confirms the shared guard in
// extractValidExpiresAt still applies on this path. A 0.4-confidence
// extraction is treated as a hallucination and must not move mail: the
// standing bias of this codebase is false negatives over false positives.
func TestApplyDeadlineCheckDropsLowConfidence(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	past := time.Now().Add(-24 * time.Hour)
	s, _ := deadlineTestScanner(t, d, past.Format(time.RFC3339), "maybe expired?", 0.4)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0}
	dest, expiresAt, _ := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Promotions",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/AI-Triage/Promotions" {
		t.Errorf("dest = %q; a low-confidence extraction must not move mail", dest)
	}
	if expiresAt != nil {
		t.Error("expiresAt was set from a below-threshold extraction")
	}
}

// TestApplyDeadlineCheckSkipsUnknownFolder covers a destination that maps to
// no category at all -- for instance a rule pointing at a folder the user
// later removed from Settings. Unknown means not perishable.
func TestApplyDeadlineCheckSkipsUnknownFolder(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	past := time.Now().Add(-24 * time.Hour)
	s, calls := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended", 0.95)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0}
	dest, _, _ := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/Some/Unmapped", s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/Some/Unmapped" {
		t.Errorf("dest = %q, want unchanged", dest)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("made %d LLM call(s) for an unmapped folder, want 0", got)
	}
}
```

Add `"github.com/kraftbj/mailreaper/internal/rules"` to the import block.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd /Users/kraft/code/mailreaper/web && go test ./internal/scanner/ -run TestApplyDeadlineCheck -v
```

Expected: compile failure — `s.applyDeadlineCheck undefined` and `s.deadlineCheckedFolders undefined`.

- [ ] **Step 3: Create `deadline.go`**

Create `web/internal/scanner/deadline.go`:

```go
package scanner

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/llm"
	"github.com/kraftbj/mailreaper/internal/rules"
)

/*
deadlineCheckedFolders returns the set of folder names belonging to
categories flagged perishable (categories.check_deadlines), keyed by
lowercased folder name.

Resolved once per scan cycle rather than per message: it is a database read,
and the answer cannot change mid-cycle in any way worth reacting to. On error
it returns nil, which every caller reads as "no folder is perishable" -- the
safe direction, since the consequence is mail staying put rather than mail
being expired on a bad lookup.
*/
func (s *Scanner) deadlineCheckedFolders() map[string]bool {
	cats, err := s.db.GetCategories()
	if err != nil {
		log.Printf("scanner: get categories for deadline-check lookup: %v", err)
		return nil
	}
	checked := map[string]bool{}
	for _, c := range cats {
		if c.CheckDeadlines && c.FolderName != "" {
			checked[strings.ToLower(c.FolderName)] = true
		}
	}
	return checked
}

/*
analyzeDeadline runs the deadline-extraction prompt for a message, reading
through the "analysis" LLM cache so a message costs at most one call ever
rather than one per scan cycle.

Extracted from evaluateLLM so the "llm" rule type and the after-routing check
(applyDeadlineCheck) share one code path. The cache key, the 2000-character
body cap, and the training-example lookup must not drift between the two
callers: a divergence there would show up as duplicate spend or as one caller
silently reading the other's cache miss.
*/
func (s *Scanner) analyzeDeadline(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, customPrompt, category string) (*llm.LLMResponse, error) {
	cacheKey := s.llmCacheKey("analysis")

	cached, err := s.db.GetCachedVerdict(msg.MessageID, cacheKey)
	if err != nil {
		return nil, fmt.Errorf("get cached verdict: %w", err)
	}
	if cached != nil {
		/* Surface the cached failure rather than silently reconstructing an
		empty response: within the 10-minute error TTL a network call is
		skipped, but the outage must stay visible in the logs the way a
		fresh failure would be. */
		if cached.Error != "" {
			return nil, fmt.Errorf("cached llm error: %s", cached.Error)
		}
		return &llm.LLMResponse{
			ExpiresAt:  cached.ExpiresAt,
			Reason:     cached.Reason,
			Confidence: cached.Confidence,
			Matches:    cached.Classified,
		}, nil
	}

	body, fetchErr := client.FetchBody(msg.Folder, msg.UID)
	if fetchErr != nil {
		log.Printf("scanner: analysis fetch body uid=%d: %v", msg.UID, fetchErr)
		body = ""
	}
	if len(body) > 2000 {
		body = body[:2000]
	}

	trainingCategory := category
	if trainingCategory == "" {
		trainingCategory = "expiry"
	}
	trainingExamples, err := s.db.GetTrainingExamples(trainingCategory)
	if err != nil {
		log.Printf("scanner: get training examples: %v", err)
	}
	refs := make([]llm.TrainingRef, 0, len(trainingExamples))
	for _, ex := range trainingExamples {
		refs = append(refs, llm.TrainingRef{Sender: ex.Sender, Subject: ex.Subject})
	}

	systemPrompt, userContent := llm.BuildAnalysisPrompt(llm.MessageData{
		Sender:       msg.Sender,
		Subject:      msg.Subject,
		SentDate:     msg.Date.Format(time.RFC3339),
		BodySnippet:  body,
		CustomPrompt: customPrompt,
		Category:     category,
	}, refs)

	result, err := llm.CallLLM(ctx, &s.cfg.LLM, systemPrompt, userContent)
	if err != nil {
		/* Cache the failure too: without this, a message that fails to
		analyze gets retried on every scan instead of backing off for the
		error TTL. */
		if cacheErr := s.db.SetCachedVerdict(msg.MessageID, cacheKey, db.CachedVerdict{Error: err.Error()}); cacheErr != nil {
			log.Printf("scanner: cache analysis error for %q: %v", msg.MessageID, cacheErr)
		}
		return nil, fmt.Errorf("call llm: %w", err)
	}

	if err := s.db.SetCachedVerdict(msg.MessageID, cacheKey, db.CachedVerdict{
		ExpiresAt:  result.ExpiresAt,
		Reason:     result.Reason,
		Confidence: result.Confidence,
	}); err != nil {
		log.Printf("scanner: cache analysis verdict for %q: %v", msg.MessageID, err)
	}
	return result, nil
}

/*
applyDeadlineCheck asks the model for a content-implied deadline on a message
that a routing rule placed in a perishable folder without one, and returns
the destination the message should actually go to, the deadline to persist,
and the reason to record.

This exists because evaluateMessage is first-match-wins: a classify rule at
priority 27 short-circuits the llm and llm-classify rules at 100 and 200+,
which are the only rules that ask about deadlines. Every one of the 4,359
classify-routed verdicts in the live database carried no deadline as a
result, so "sale ends Friday" sat in Promotions forever.

It runs after routing rather than before it so that llm-classify messages,
which already receive a deadline from their combined classify+extract call,
pay nothing extra.

Every path that does not apply -- an existing deadline, a non-perishable or
unmapped destination, provider "none", an LLM error, no deadline found, an
extraction rejected by extractValidExpiresAt's confidence and plausibility
guards -- returns dest unchanged and a nil deadline, so the caller behaves
exactly as it does today.
*/
func (s *Scanner) applyDeadlineCheck(
	ctx context.Context,
	client MailClient,
	msg *imappkg.FetchedMessage,
	verdict *rules.RuleVerdict,
	dest string,
	checked map[string]bool,
	expiredFolder string,
) (string, *time.Time, string) {
	if verdict == nil {
		return dest, nil, ""
	}
	if verdict.ExpiresAt != nil {
		return dest, verdict.ExpiresAt, verdict.Reason
	}
	if !checked[strings.ToLower(dest)] {
		return dest, nil, verdict.Reason
	}
	if strings.ToLower(s.cfg.LLM.Provider) == "none" {
		return dest, nil, verdict.Reason
	}

	result, err := s.analyzeDeadline(ctx, client, msg, "", "")
	if err != nil {
		log.Printf("scanner: deadline check for %q: %v", msg.MessageID, err)
		return dest, nil, verdict.Reason
	}

	expiresAt, past, valid := extractValidExpiresAt(result.ExpiresAt, result.Confidence, s.loc, msg.Date)
	if !valid {
		return dest, nil, verdict.Reason
	}

	if past {
		routed := dest
		if expiredFolder != "" {
			routed = expiredFolder
		}
		log.Printf("scanner: %q has a past deadline (%s); routing to %q instead of %q",
			msg.Subject, expiresAt.Format(time.RFC3339), routed, dest)
		return routed, expiresAt, result.Reason
	}

	log.Printf("scanner: %q keeps folder %q with a deferred deadline of %s",
		msg.Subject, dest, expiresAt.Format(time.RFC3339))
	return dest, expiresAt, result.Reason
}
```

- [ ] **Step 4: Run the new tests to verify they pass**

```bash
cd /Users/kraft/code/mailreaper/web && gofmt -w internal/scanner/ && go test ./internal/scanner/ -run 'TestApplyDeadlineCheck|TestDeadlineChecked' -v
```

Expected: PASS — all seven.

- [ ] **Step 5: Refactor `evaluateLLM` onto the shared helper**

In `web/internal/scanner/scanner.go`, replace the whole body of `evaluateLLM` with the version below. Its doc comment keeps the existing return-value contract, which callers rely on; only the mechanism changes.

```go
// evaluateLLM handles "llm" rule types (deadline-only extraction). The cache
// read, body fetch, and prompt call live in analyzeDeadline (deadline.go),
// shared with the after-routing deadline check so the two cannot drift.
//
// Returns:
//   - past extracted deadline → expired verdict routed to canonical Expired folder
//   - future extracted deadline → nil (the `llm` rule type has no category to
//     park the message under; the next scan with a then-past deadline will
//     re-evaluate from cache and produce the verdict)
//   - no deadline → nil
func (s *Scanner) evaluateLLM(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, rule db.Rule, expiredFolder string) (*rules.RuleVerdict, error) {
	result, err := s.analyzeDeadline(ctx, client, msg, rule.ExpirationConfig.Prompt, rule.ExpirationConfig.Category)
	if err != nil {
		return nil, err
	}

	expiresAt, past, hasDeadline := extractValidExpiresAt(result.ExpiresAt, result.Confidence, s.loc, msg.Date)
	if !hasDeadline || !past {
		return nil, nil
	}

	expiredRule := rule
	if expiredFolder != "" {
		expiredRule.DestinationFolder = expiredFolder
	}
	return &rules.RuleVerdict{
		Expired:    true,
		Rule:       expiredRule,
		ExpiresAt:  expiresAt,
		Reason:     result.Reason,
		Confidence: result.Confidence,
	}, nil
}
```

Run the full scanner suite now, before wiring anything else — the existing `TestBackfillClearsCachedVerdict` and `TestBackfillNilVerdictFromLLMFailureDoesNotRescue` exercise this exact path and will catch a botched refactor:

```bash
cd /Users/kraft/code/mailreaper/web && go test ./internal/scanner/ -v 2>&1 | tail -30
```

Expected: PASS.

- [ ] **Step 6: Wire the check into `ScanAccount`**

In `web/internal/scanner/scanner.go`, in `ScanAccount`, resolve the perishable set once per cycle, right after `expiredFolder`:

```go
	expiredFolder := s.canonicalExpiredFolder()
	deadlineChecked := s.deadlineCheckedFolders()
```

Then, inside the `if verdict.Confidence >= 0.7 {` branch, after the three lines that resolve `dest` and before the `ensureErr := client.EnsureFolder(dest)` line, insert:

```go
				/* A routing rule picked the folder; if it supplied no
				deadline and that folder is perishable, ask for one now. A
				past deadline overrides the folder. See applyDeadlineCheck. */
				var deadline *time.Time
				dest, deadline, v.Reason = s.applyDeadlineCheck(ctx, client, &msg, verdict, dest, deadlineChecked, expiredFolder)
				v.ExpiresAt = deadline
```

Finally, in the same branch, replace the activity-type assignment so a message redirected to Expired is logged as expired rather than triaged:

```go
				activityType := "triaged"
				if verdict.Expired || (expiredFolder != "" && strings.EqualFold(dest, expiredFolder)) {
					activityType = "expired"
				}
```

The extraction runs only on this branch by design. A low-confidence verdict is queued `pending` and the message stays in the inbox, so there is nothing in a triage folder to expire and no reason to spend a call on it.

- [ ] **Step 7: Write and run an end-to-end scan test**

Append to `web/internal/scanner/deadline_test.go`:

```go
// TestScanCycleClassifyRuleGetsDeadlineCheck is the end-to-end proof, run
// through ScanAccount rather than applyDeadlineCheck directly: a priority-27
// classify rule of the kind DistillRules generates routes a message to
// Promotions, and the past deadline in its body must redirect it to Expired.
//
// This is the exact shape of the reported bug -- a dmsguild or Cinemark
// sender rule filing a dated sale email into a triage folder it never leaves.
func TestScanCycleClassifyRuleGetsDeadlineCheck(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := d.SaveCategory(db.Category{
		ID: "expired", Name: "Expired", FolderName: "Folders/AI-Triage/Expired",
	}); err != nil {
		t.Fatalf("save expired category: %v", err)
	}
	if err := d.SaveRule(db.Rule{
		ID:                "auto-shop-example-test",
		Name:              "shop@example.test → Promotions",
		Enabled:           true,
		Priority:          27,
		MatchConfig:       db.MatchConfig{SenderPatterns: []string{"*@example.test"}},
		ExpirationConfig:  db.ExpirationConfig{Type: "classify"},
		DestinationFolder: "Folders/AI-Triage/Promotions",
		Action:            "move",
	}); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	s, _ := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended Friday", 0.95)

	msg := deadlineTestMessage()
	client := &mockMailClient{messages: []imappkg.FetchedMessage{*msg}}

	if err := s.ScanAccount(context.Background(), client, "acct", []string{"INBOX"}); err != nil {
		t.Fatalf("ScanAccount: %v", err)
	}

	if len(client.movedMsgs) != 1 {
		t.Fatalf("recorded %d move(s), want 1", len(client.movedMsgs))
	}
	if client.movedMsgs[0] != "Folders/AI-Triage/Expired" {
		t.Errorf("moved to %q, want Folders/AI-Triage/Expired", client.movedMsgs[0])
	}

	v, err := d.GetVerdictByMessageID(msg.MessageID)
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected a verdict, got nil")
	}
	if v.DestinationFolder != "Folders/AI-Triage/Expired" {
		t.Errorf("DestinationFolder = %q, want Folders/AI-Triage/Expired", v.DestinationFolder)
	}
	if v.ExpiresAt == nil {
		t.Error("ExpiresAt was not persisted")
	}

	actLog, err := d.GetActivityLog(10)
	if err != nil {
		t.Fatalf("GetActivityLog: %v", err)
	}
	if len(actLog) != 1 {
		t.Fatalf("expected 1 activity entry, got %d", len(actLog))
	}
	if actLog[0].Type != "expired" {
		t.Errorf("activity Type = %q, want expired; a redirect to Expired is an expiry, not a triage", actLog[0].Type)
	}
}
```

```bash
cd /Users/kraft/code/mailreaper/web && gofmt -w internal/scanner/ && go test ./... 2>&1 | tail -20
```

Expected: PASS across the module.

- [ ] **Step 8: Commit**

```bash
cd /Users/kraft/code/mailreaper/web
git add internal/scanner/deadline.go internal/scanner/deadline_test.go internal/scanner/scanner.go
git commit -m "feat: extract a deadline for mail routed into a perishable folder

evaluateMessage is first-match-wins, and EvaluateClassify returns a
verdict with no body fetch and no LLM call. A classify rule at priority
27 therefore short-circuits the llm and llm-classify rules at 100 and
200+, which are the only rules that ask about deadlines -- so all 4,359
classify-routed verdicts in the live database carry none, and 'sale ends
Friday' sits in Promotions forever.

After a rule picks a folder, if the verdict has no deadline and the
folder belongs to a category flagged check_deadlines, one cached call
extracts one. Past deadlines override the folder to Expired; future ones
are persisted for the sweep. Running after routing rather than before it
means llm-classify messages, which already get a deadline from their
combined call, pay nothing extra.

Also factors the cache-and-prompt half of evaluateLLM into
analyzeDeadline, shared by both callers so they cannot drift."
```

---

### Task 5: Extend the backfill to catch up existing mail

**Files:**
- Modify: `web/internal/scanner/backfill.go`
- Test: `web/internal/scanner/backfill_test.go`

**Interfaces:**
- Consumes: `s.deadlineCheckedFolders()` and `s.applyDeadlineCheck(...)` from Task 4.
- Produces: nothing later tasks depend on. This is the last task.

`BackfillFolders` already walks the live IMAP folders via `GetMessagesInFolder`, so it covers what is actually present today rather than the historical verdict count — much of that history has since been deleted or archived. It also already calls `RemoveCachedVerdict` before re-evaluating, so the extraction is a genuine re-ask rather than a cache replay.

- [ ] **Step 1: Write the failing tests**

Append to `web/internal/scanner/backfill_test.go`:

```go
// TestBackfillExtractsDeadlineInPerishableFolder covers the catch-up case:
// a message already sitting in Promotions, routed there by a classify rule
// that never asked about deadlines, whose body says the sale is over. The
// backfill must pull it into Expired.
//
// This is also how manually-filed mail gets caught: those verdicts were
// written by DetectManualClassifications and never carried a deadline, but
// the messages are physically in the folder.
func TestBackfillExtractsDeadlineInPerishableFolder(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := d.SaveCategory(db.Category{
		ID: "expired", Name: "Expired", FolderName: "Folders/AI-Triage/Expired",
	}); err != nil {
		t.Fatalf("save expired category: %v", err)
	}
	if err := d.SaveRule(db.Rule{
		ID:                "auto-shop-example-test",
		Name:              "shop@example.test → Promotions",
		Enabled:           true,
		Priority:          27,
		MatchConfig:       db.MatchConfig{SenderPatterns: []string{"*@example.test"}},
		ExpirationConfig:  db.ExpirationConfig{Type: "classify"},
		DestinationFolder: "Folders/AI-Triage/Promotions",
		Action:            "move",
	}); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	s, _ := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended Friday", 0.95)

	msg := imappkg.FetchedMessage{
		UID:       1,
		MessageID: "<stale-promo@test>",
		Subject:   "Sale ends Friday",
		Sender:    "shop@example.test",
		Date:      time.Now().Add(-96 * time.Hour),
		Folder:    "Folders/AI-Triage/Promotions",
	}
	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {msg},
		},
	}

	if _, err := s.BackfillFolders(context.Background(), client, "acct", "INBOX"); err != nil {
		t.Fatalf("BackfillFolders: %v", err)
	}

	if len(client.movedMsgs) != 1 {
		t.Fatalf("recorded %d move(s), want 1", len(client.movedMsgs))
	}
	if client.movedMsgs[0] != "Folders/AI-Triage/Expired" {
		t.Errorf("moved to %q, want Folders/AI-Triage/Expired", client.movedMsgs[0])
	}

	v, err := d.GetVerdictByMessageID("<stale-promo@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected a verdict, got nil")
	}
	if v.ExpiresAt == nil {
		t.Error("ExpiresAt was not persisted by the backfill")
	}
}

// TestBackfillSkipsDeadlineCheckInNonPerishableFolder asserts the flag gates
// the backfill too, on the call count. Backfill walks entire folders, so an
// ungated pass would spend a call on every message in Newsletters -- the
// single largest population, and one that never expires.
func TestBackfillSkipsDeadlineCheckInNonPerishableFolder(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := d.SaveCategory(db.Category{
		ID: "expired", Name: "Expired", FolderName: "Folders/AI-Triage/Expired",
	}); err != nil {
		t.Fatalf("save expired category: %v", err)
	}
	if err := d.SaveRule(db.Rule{
		ID:                "auto-news-example-test",
		Name:              "news@example.test → Newsletters",
		Enabled:           true,
		Priority:          27,
		MatchConfig:       db.MatchConfig{SenderPatterns: []string{"*@example.test"}},
		ExpirationConfig:  db.ExpirationConfig{Type: "classify"},
		DestinationFolder: "Folders/AI-Triage/Newsletters",
		Action:            "move",
	}); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	s, calls := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended", 0.95)

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Newsletters": {{
				UID:       1,
				MessageID: "<weekly@test>",
				Subject:   "This week's issue",
				Sender:    "news@example.test",
				Date:      time.Now().Add(-96 * time.Hour),
				Folder:    "Folders/AI-Triage/Newsletters",
			}},
		},
	}

	if _, err := s.BackfillFolders(context.Background(), client, "acct", "INBOX"); err != nil {
		t.Fatalf("BackfillFolders: %v", err)
	}

	if len(client.movedMsgs) != 0 {
		t.Errorf("recorded %d move(s) out of a non-perishable folder, want 0", len(client.movedMsgs))
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("made %d LLM call(s) in a non-perishable folder, want 0", got)
	}
}
```

`backfill_test.go` already imports everything these need (`context`, `sync/atomic`, `time`, `db`, `imappkg`), and `deadlineTestScanner` / `seedDeadlineCategories` come from `deadline_test.go` in the same package. No import changes.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd /Users/kraft/code/mailreaper/web && go test ./internal/scanner/ -run TestBackfill -v
```

Expected: `TestBackfillExtractsDeadlineInPerishableFolder` FAILS with `recorded 0 move(s), want 1` — the classify rule keeps the message in Promotions. `TestBackfillSkipsDeadlineCheckInNonPerishableFolder` passes already; keep it as the regression guard for Step 3.

- [ ] **Step 3: Wire the check into `BackfillFolders`**

In `web/internal/scanner/backfill.go`, resolve the set once, right after `expiredFolder`:

```go
	expiredFolder := s.canonicalExpiredFolder()
	deadlineChecked := s.deadlineCheckedFolders()
```

Then, inside the per-message loop, after the `dest := backfillDestination(...)` line and the `confidence` block that follows it, and **before** the `if strings.EqualFold(dest, folder)` "Kept" check, insert:

```go
			/* Ordered ahead of the Kept check on purpose: a message whose
			deadline is still in the future keeps its current folder, which
			means the Kept branch claims it. Running the check after that
			would leave exactly the mail this backfill exists to catch --
			everything already sitting in a perishable folder -- without a
			deadline. */
			if confidence >= 0.7 {
				var deadline *time.Time
				dest, deadline, _ = s.applyDeadlineCheck(ctx, client, &msg, verdict, dest, deadlineChecked, expiredFolder)
				if deadline != nil && verdict != nil {
					verdict.ExpiresAt = deadline
				}
			}
```

`saveBackfillVerdict` already copies `v.ExpiresAt` onto the row, so both the moved and the Kept paths persist the deadline with no further change.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd /Users/kraft/code/mailreaper/web && gofmt -w internal/scanner/ && go test ./... 2>&1 | tail -20
```

Expected: PASS across the module.

- [ ] **Step 5: Commit**

```bash
cd /Users/kraft/code/mailreaper/web
git add internal/scanner/backfill.go internal/scanner/backfill_test.go
git commit -m "feat: backfill extracts deadlines for mail already in triage folders

BackfillFolders walks the live IMAP folders, so it catches up exactly
what is sitting there now -- including manually-filed mail, whose
verdicts never carried a deadline but whose messages are physically in
the folder.

The check runs ahead of the Kept branch deliberately: a still-future
deadline keeps the current folder, so running it afterward would skip
precisely the mail this pass exists to reach."
```

---

## Verification against the live system

After all five tasks land, in the order the spec's rollout section gives:

- [ ] **Confirm the flags.** Open Settings. Promotions, Notifications, and Hobbies show Deadlines = Yes; Newsletters, Paper-Trail, and Expired show "—".

- [ ] **Confirm the sweep drains.** Before restarting, record the backlog:

```bash
cd /Users/kraft/code/mailreaper/web
sqlite3 -header -column mailreaper.db "
  SELECT destination_folder, COUNT(*) FROM verdicts
  WHERE expires_at IS NOT NULL AND expires_at != ''
    AND expires_at < datetime('now')
    AND status IN ('executed','manual')
    AND destination_folder NOT LIKE '%Expired%'
  GROUP BY 1;"
```

Baseline as of 2026-08-31: Notifications 94, Promotions 87, Hobbies 8, Newsletters 2 — 191 rows. Run one scan cycle, then re-run the query. Every row should be gone: moved to Expired, or `orphaned` if its message had already left the folder. Check the split:

```bash
sqlite3 -column mailreaper.db "SELECT status, COUNT(*) FROM verdicts WHERE status = 'orphaned' GROUP BY 1;"
```

A result of all-orphaned and nothing moved would mean the messages are not where the verdicts say — worth investigating before trusting the extraction pass.

- [ ] **Watch one live scan with extraction on.** Confirm the log shows `has a past deadline (...); routing to` lines for perishable folders and none for Newsletters or Paper-Trail.

- [ ] **Run the backfill.**

```bash
cd /Users/kraft/code/mailreaper/web && go run ./cmd/mailreaper -backfill
```

Then confirm the deadline coverage of classify-routed verdicts is no longer zero:

```bash
sqlite3 -header -column mailreaper.db "
  SELECT json_extract(r.expiration_config,'\$.type') typ,
         COUNT(*) n,
         SUM(CASE WHEN v.expires_at IS NOT NULL AND v.expires_at != '' THEN 1 ELSE 0 END) with_exp
  FROM verdicts v LEFT JOIN rules r ON r.id = v.rule_id
  WHERE v.status = 'executed' GROUP BY 1;"
```

The `classify` row's `with_exp` was 0 out of 4,359 before this work. It will not reach 4,359 — most promotional mail genuinely has no hard deadline, and Newsletters and Paper-Trail are excluded by design — but it must no longer be zero.
