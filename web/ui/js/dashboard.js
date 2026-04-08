/**
 * dashboard.js — logic for the main dashboard page.
 */
import { api, connectSSE, timeAgo, fmtConfidence, badgeClass } from "./app.js";

let closeSSE = null;

async function loadDashboard() {
  const [stats, activity, verdicts, categories] = await Promise.all([
    api("/api/stats").catch(() => ({})),
    api("/api/activity?limit=25").catch(() => []),
    api("/api/verdicts/pending").catch(() => []),
    api("/api/categories").catch(() => []),
  ]);

  renderStats(stats);
  renderTriageGrid(categories);
  renderActivity(activity);
  renderReviewQueue(verdicts);
}

function renderStats(stats) {
  const el = document.getElementById("stats-row");
  if (!el) return;

  el.innerHTML = `
    <div class="stat-card">
      <div class="label">Triaged Today</div>
      <div class="value green">${stats.movedToday ?? 0}</div>
    </div>
    <div class="stat-card">
      <div class="label">Awaiting Review</div>
      <div class="value blue">${stats.pendingVerdicts ?? 0}</div>
    </div>
    <div class="stat-card">
      <div class="label">Corrections</div>
      <div class="value accent">${stats.correctedToday ?? 0}</div>
    </div>
  `;
}

function renderTriageGrid(categories) {
  const el = document.getElementById("triage-grid");
  if (!el) return;

  if (categories.length === 0) {
    el.innerHTML = `<div class="text-muted" style="grid-column:1/-1;padding:8px 0;font-size:13px;">No categories configured. Add them in <a href="/settings.html">Settings</a>.</div>`;
    return;
  }

  el.innerHTML = categories.map((cat) => `
    <div class="triage-card">
      <div class="icon">${cat.icon || "📁"}</div>
      <div class="name">${esc(cat.name)}</div>
      <div class="count">${esc(cat.folderName || "")}</div>
    </div>
  `).join("");
}

function renderActivity(entries) {
  const el = document.getElementById("activity-list");
  if (!el) return;

  if (!entries || entries.length === 0) {
    el.innerHTML = `<div class="empty">No recent activity.</div>`;
    return;
  }

  el.innerHTML = `<ul class="activity-list">` +
    entries.map((e) => `
      <li class="activity-item">
        <span class="activity-badge ${badgeClass(e.type)}">${esc(e.type)}</span>
        <div class="activity-content">
          <div class="activity-subject" title="${esc(e.subject)}">${esc(e.subject || "(no subject)")}</div>
          <div class="activity-meta">${esc(e.sender || "")}${e.ruleName ? ` · ${esc(e.ruleName)}` : ""}</div>
        </div>
        <span class="activity-time">${timeAgo(e.createdAt)}</span>
      </li>
    `).join("") +
    `</ul>`;
}

function renderReviewQueue(verdicts) {
  const el = document.getElementById("review-queue");
  const badge = document.getElementById("review-count");
  if (!el) return;

  if (badge) badge.textContent = verdicts.length || "";

  if (!verdicts || verdicts.length === 0) {
    el.innerHTML = `<div class="empty">Nothing to review.</div>`;
    return;
  }

  // Show at most 5 in the dashboard panel.
  const shown = verdicts.slice(0, 5);
  el.innerHTML = `<ul class="verdict-list">` +
    shown.map((v) => `
      <li class="verdict-item">
        <div class="verdict-subject" title="${esc(v.subject)}">${esc(v.subject || "(no subject)")}</div>
        <div class="verdict-meta">
          ${esc(v.sender || "")}
          <span class="verdict-confidence">${fmtConfidence(v.confidence)}</span>
        </div>
        <div class="verdict-actions">
          <button class="btn btn-sm btn-success" onclick="approveVerdict(${JSON.stringify(v.messageIdHeader)})">Approve</button>
          <button class="btn btn-sm btn-danger"  onclick="rejectVerdict(${JSON.stringify(v.messageIdHeader)})">Reject</button>
        </div>
      </li>
    `).join("") +
    `</ul>` +
    (verdicts.length > 5 ? `<div style="padding:10px 16px;border-top:1px solid var(--border);font-size:12px;"><a href="/review.html">View all ${verdicts.length} →</a></div>` : "");
}

window.approveVerdict = async function(msgId) {
  await updateVerdict(msgId, "approved");
};

window.rejectVerdict = async function(msgId) {
  await updateVerdict(msgId, "rejected");
};

async function updateVerdict(msgId, status) {
  try {
    await api(`/api/verdicts/${encodeURIComponent(msgId)}/status`, {
      method: "PUT",
      body: JSON.stringify({ status }),
    });
    await loadDashboard();
  } catch (err) {
    console.error("Failed to update verdict:", err);
  }
}

function esc(str) {
  if (!str) return "";
  return String(str)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function updateScanStatus(text, ok = true) {
  const dot = document.getElementById("status-dot");
  const label = document.getElementById("status-label");
  if (dot) dot.className = `status-dot ${ok ? "ok" : "error"}`;
  if (label) label.textContent = text;
}

document.addEventListener("DOMContentLoaded", () => {
  loadDashboard();

  closeSSE = connectSSE(({ type }) => {
    if (type === "scan_complete") {
      updateScanStatus("Last scan: just now");
      loadDashboard();
    }
  });
});

window.addEventListener("beforeunload", () => {
  if (closeSSE) closeSSE();
});
