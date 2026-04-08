/**
 * review.js — full review queue page.
 */
import { api, connectSSE, timeAgo, fmtConfidence } from "./app.js";

let allVerdicts = [];
let closeSSE = null;

async function loadVerdicts() {
  allVerdicts = await api("/api/verdicts/pending").catch(() => []);
  renderTable();
}

function sortKey() {
  return document.getElementById("sort-select")?.value || "confidence-desc";
}

function sorted(verdicts) {
  const key = sortKey();
  const copy = [...verdicts];
  switch (key) {
    case "confidence-desc": return copy.sort((a, b) => (b.confidence ?? 0) - (a.confidence ?? 0));
    case "confidence-asc":  return copy.sort((a, b) => (a.confidence ?? 0) - (b.confidence ?? 0));
    case "date-desc":       return copy.sort((a, b) => new Date(b.sentAt) - new Date(a.sentAt));
    case "date-asc":        return copy.sort((a, b) => new Date(a.sentAt) - new Date(b.sentAt));
    default:                return copy;
  }
}

function renderTable() {
  const tbody = document.getElementById("verdict-tbody");
  if (!tbody) return;

  const rows = sorted(allVerdicts);

  if (rows.length === 0) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty">No pending verdicts.</td></tr>`;
    return;
  }

  tbody.innerHTML = rows.map((v) => {
    const conf = v.confidence ?? 0;
    const confPct = Math.round(conf * 100);
    return `
      <tr>
        <td style="max-width:280px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis" title="${esc(v.subject)}">${esc(v.subject || "(no subject)")}</td>
        <td style="max-width:200px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis" title="${esc(v.sender)}">${esc(v.sender || "")}</td>
        <td style="white-space:nowrap">${v.sentAt ? timeAgo(v.sentAt) : "—"}</td>
        <td style="max-width:240px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis" title="${esc(v.reason)}">${esc(v.reason || "")}</td>
        <td style="white-space:nowrap">
          <div class="conf-bar" style="width:120px">
            <div class="conf-bar-track"><div class="conf-bar-fill" style="width:${confPct}%"></div></div>
            <span class="conf-label">${confPct}%</span>
          </div>
        </td>
        <td>
          <div class="flex gap-8">
            <button class="btn btn-sm btn-success" data-action="approve" data-id="${esc(v.messageIdHeader)}">Approve</button>
            <button class="btn btn-sm btn-danger"  data-action="reject"  data-id="${esc(v.messageIdHeader)}">Reject</button>
          </div>
        </td>
      </tr>
    `;
  }).join("");
}

async function updateVerdict(msgId, status) {
  await api(`/api/verdicts/${encodeURIComponent(msgId)}/status`, {
    method: "PUT",
    body: JSON.stringify({ status }),
  });
  await loadVerdicts();
}

async function batchApprove() {
  const thresholdEl = document.getElementById("threshold-input");
  const threshold = parseFloat(thresholdEl?.value || "90") / 100;
  const eligible = allVerdicts.filter((v) => (v.confidence ?? 0) >= threshold);
  if (eligible.length === 0) {
    alert("No verdicts meet that confidence threshold.");
    return;
  }
  const btn = document.getElementById("batch-approve-btn");
  if (btn) btn.disabled = true;
  try {
    await Promise.all(
      eligible.map((v) =>
        api(`/api/verdicts/${encodeURIComponent(v.messageIdHeader)}/status`, {
          method: "PUT",
          body: JSON.stringify({ status: "approved" }),
        })
      )
    );
    await loadVerdicts();
  } finally {
    if (btn) btn.disabled = false;
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

document.addEventListener("DOMContentLoaded", () => {
  loadVerdicts();

  document.getElementById("sort-select")?.addEventListener("change", renderTable);

  document.getElementById("batch-approve-btn")?.addEventListener("click", batchApprove);

  document.getElementById("verdict-tbody")?.addEventListener("click", async (e) => {
    const btn = e.target.closest("button[data-action]");
    if (!btn) return;
    const action = btn.dataset.action;
    const id = btn.dataset.id;
    btn.disabled = true;
    try {
      await updateVerdict(id, action === "approve" ? "approved" : "rejected");
    } finally {
      btn.disabled = false;
    }
  });

  closeSSE = connectSSE(({ type }) => {
    if (type === "scan_complete") loadVerdicts();
  });
});

window.addEventListener("beforeunload", () => {
  if (closeSSE) closeSSE();
});
