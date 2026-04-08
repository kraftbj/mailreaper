/**
 * rules.js — rules management page.
 */
import { api } from "./app.js";

let allRules = [];

async function loadRules() {
  allRules = await api("/api/rules").catch(() => []);
  allRules.sort((a, b) => a.priority - b.priority);
  renderRules();
}

function typeBadge(type) {
  const map = {
    ttl: "type-ttl",
    header: "type-header",
    "content-regex": "type-regex",
    classify: "type-classify",
    llm: "type-llm",
    "llm-classify": "type-llm",
  };
  const cls = map[type] || "type-ttl";
  return `<span class="type-badge ${cls}">${esc(type)}</span>`;
}

function renderRules() {
  const tbody = document.getElementById("rules-tbody");
  if (!tbody) return;

  if (allRules.length === 0) {
    tbody.innerHTML = `<tr><td colspan="6" class="empty">No rules found.</td></tr>`;
    return;
  }

  tbody.innerHTML = allRules.map((r) => `
    <tr data-id="${esc(r.id)}">
      <td><span class="priority-badge">${r.priority}</span></td>
      <td>
        ${esc(r.name)}
        ${r.builtin ? `<span class="type-badge" style="background:rgba(255,255,255,0.06);color:var(--text-muted);margin-left:6px">built-in</span>` : ""}
      </td>
      <td>${typeBadge(r.expirationConfig?.type || r.ExpirationConfig?.type || "")}</td>
      <td>${esc(r.destinationFolder || r.DestinationFolder || "Expired")}</td>
      <td>
        <label class="toggle">
          <input type="checkbox" ${r.enabled || r.Enabled ? "checked" : ""} data-toggle-id="${esc(r.id)}">
          <span class="toggle-slider"></span>
        </label>
      </td>
      <td>
        <div class="flex gap-8">
          ${!r.builtin ? `<button class="btn btn-sm btn-outline" data-edit-id="${esc(r.id)}">Edit</button>` : ""}
          ${!r.builtin ? `<button class="btn btn-sm btn-danger" data-delete-id="${esc(r.id)}">Delete</button>` : ""}
        </div>
      </td>
    </tr>
  `).join("");
}

function openModal(rule = null) {
  const modal = document.getElementById("rule-modal");
  const title = document.getElementById("modal-title");
  if (!modal) return;

  document.getElementById("rule-id").value = rule?.id || "";
  document.getElementById("rule-name").value = rule?.name || "";
  document.getElementById("rule-priority").value = rule?.priority || 50;
  document.getElementById("rule-sender-patterns").value =
    (rule?.matchConfig?.senderPatterns || []).join("\n");
  document.getElementById("rule-subject-patterns").value =
    (rule?.matchConfig?.subjectPatterns || []).join("\n");
  document.getElementById("rule-type").value = rule?.expirationConfig?.type || "ttl";
  document.getElementById("rule-hours").value = rule?.expirationConfig?.hours || 24;
  document.getElementById("rule-destination").value = rule?.destinationFolder || "";
  document.getElementById("rule-grace").value = rule?.gracePeriodDays ?? 3;

  title.textContent = rule ? "Edit Rule" : "New Rule";
  updateTypeFields();
  modal.classList.remove("hidden");
}

function closeModal() {
  document.getElementById("rule-modal")?.classList.add("hidden");
}

function updateTypeFields() {
  const type = document.getElementById("rule-type")?.value;
  const hoursGroup = document.getElementById("ttl-hours-group");
  if (hoursGroup) {
    hoursGroup.classList.toggle("hidden", type !== "ttl");
  }
}

async function saveRule() {
  const id = document.getElementById("rule-id").value;
  const name = document.getElementById("rule-name").value.trim();
  if (!name) { alert("Rule name is required."); return; }

  const type = document.getElementById("rule-type").value;
  const senderPatterns = document.getElementById("rule-sender-patterns").value
    .split("\n").map((s) => s.trim()).filter(Boolean);
  const subjectPatterns = document.getElementById("rule-subject-patterns").value
    .split("\n").map((s) => s.trim()).filter(Boolean);

  const rule = {
    id: id || `user-${Date.now()}`,
    name,
    enabled: true,
    priority: parseInt(document.getElementById("rule-priority").value, 10) || 50,
    builtin: false,
    matchConfig: {
      senderPatterns,
      subjectPatterns,
      folders: [],
      headerMatch: {},
    },
    expirationConfig: {
      type,
      hours: type === "ttl" ? parseInt(document.getElementById("rule-hours").value, 10) || 24 : 0,
    },
    action: "move",
    destinationFolder: document.getElementById("rule-destination").value.trim(),
    gracePeriodDays: parseInt(document.getElementById("rule-grace").value, 10) || 0,
  };

  try {
    await api("/api/rules", { method: "POST", body: JSON.stringify(rule) });
    closeModal();
    await loadRules();
  } catch (err) {
    alert(`Failed to save rule: ${err.message}`);
  }
}

async function toggleRule(id, enabled) {
  const rule = allRules.find((r) => r.id === id);
  if (!rule) return;
  const updated = { ...rule, enabled };
  try {
    await api("/api/rules", { method: "POST", body: JSON.stringify(updated) });
    await loadRules();
  } catch (err) {
    console.error("Failed to toggle rule:", err);
    await loadRules(); // revert UI
  }
}

async function deleteRule(id) {
  if (!confirm("Delete this rule?")) return;
  try {
    await api(`/api/rules/${encodeURIComponent(id)}`, { method: "DELETE" });
    await loadRules();
  } catch (err) {
    alert(`Failed to delete rule: ${err.message}`);
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
  loadRules();

  document.getElementById("new-rule-btn")?.addEventListener("click", () => openModal());
  document.getElementById("modal-cancel-btn")?.addEventListener("click", closeModal);
  document.getElementById("modal-save-btn")?.addEventListener("click", saveRule);
  document.getElementById("rule-type")?.addEventListener("change", updateTypeFields);

  // Close on backdrop click
  document.getElementById("rule-modal")?.addEventListener("click", (e) => {
    if (e.target === e.currentTarget) closeModal();
  });

  // Delegate table events
  document.getElementById("rules-tbody")?.addEventListener("click", async (e) => {
    const editBtn = e.target.closest("[data-edit-id]");
    const deleteBtn = e.target.closest("[data-delete-id]");
    if (editBtn) {
      const rule = allRules.find((r) => r.id === editBtn.dataset.editId);
      if (rule) openModal(rule);
    } else if (deleteBtn) {
      await deleteRule(deleteBtn.dataset.deleteId);
    }
  });

  document.getElementById("rules-tbody")?.addEventListener("change", async (e) => {
    const toggle = e.target.closest("[data-toggle-id]");
    if (toggle) {
      await toggleRule(toggle.dataset.toggleId, toggle.checked);
    }
  });
});
