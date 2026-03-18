/**
 * Options page script — manages settings, rules, LLM config, folders, and activity.
 */

import { saveRule, deleteRule, getRules, reorderRules, resetRulesToDefaults } from "../rules/storage.js";

let currentSettings = {};
let currentRules = [];
let editingRuleId = null;

// ── Tab Navigation ──────────────────────────────────────────────────────────

document.querySelectorAll(".tab").forEach((tab) => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
    document.querySelectorAll(".tab-content").forEach((c) => c.classList.remove("active"));
    tab.classList.add("active");
    document.getElementById(`tab-${tab.dataset.tab}`).classList.add("active");

    // Lazy-load tab content
    if (tab.dataset.tab === "folders") loadFolders();
    if (tab.dataset.tab === "activity") loadActivity();
  });
});

// ── General Tab ─────────────────────────────────────────────────────────────

async function loadGeneral() {
  currentSettings = await messenger.runtime.sendMessage({ type: "getSettings" });

  document.getElementById("scanEnabled").checked = currentSettings.scanEnabled;
  document.getElementById("scanInterval").value = currentSettings.scanIntervalMinutes;
  document.getElementById("minAge").value = currentSettings.minMessageAgeMinutes;
  document.getElementById("maxPerScan").value = currentSettings.maxMessagesPerScan;
  document.getElementById("gracePeriod").value = currentSettings.defaultGracePeriodDays;
  document.getElementById("graceCleanup").checked = currentSettings.gracePeriodCleanupEnabled;
  document.getElementById("notifyExpiration").checked = currentSettings.notifyOnExpiration;
}

document.getElementById("btnSaveGeneral").addEventListener("click", async () => {
  try {
    await messenger.runtime.sendMessage({
      type: "updateSettings",
      settings: {
        scanEnabled: document.getElementById("scanEnabled").checked,
        scanIntervalMinutes: parseInt(document.getElementById("scanInterval").value),
        minMessageAgeMinutes: parseInt(document.getElementById("minAge").value),
        maxMessagesPerScan: parseInt(document.getElementById("maxPerScan").value),
        defaultGracePeriodDays: parseInt(document.getElementById("gracePeriod").value),
        gracePeriodCleanupEnabled: document.getElementById("graceCleanup").checked,
        notifyOnExpiration: document.getElementById("notifyExpiration").checked,
      },
    });
    showSaveStatus("saveStatus");
  } catch (e) {
    console.error("Failed to save general settings:", e);
    showSaveStatus("saveStatus", "Save failed");
  }
});

// ── Rules Tab ───────────────────────────────────────────────────────────────

async function loadRules() {
  currentRules = await messenger.runtime.sendMessage({ type: "getRules" });
  renderRulesList();
}

function renderRulesList() {
  const container = document.getElementById("rulesList");
  const sorted = [...currentRules].sort((a, b) => a.priority - b.priority);

  if (sorted.length === 0) {
    container.innerHTML = '<div class="empty-state">No rules configured.</div>';
    return;
  }

  container.innerHTML = sorted
    .map((rule) => {
      const typeLabel = {
        ttl: `${rule.expiration.hours}h TTL`,
        header: "Expires header",
        llm: "AI analysis",
        "content-regex": "Regex",
        classify: `→ ${rule.classifyFolder || "Paper-Trail"}`,
        "llm-classify": `AI → ${rule.classifyFolder || "Paper-Trail"}`,
      }[rule.expiration.type] || rule.expiration.type;

      const badges = [];
      if (rule.expiration.type === "llm" || rule.expiration.type === "llm-classify") badges.push('<span class="rule-badge llm">AI</span>');
      if (rule.builtin) badges.push('<span class="rule-badge builtin">Built-in</span>');

      return `
        <div class="rule-item ${rule.enabled ? "" : "disabled"}" data-id="${rule.id}">
          <span class="rule-drag">⠿</span>
          <input type="checkbox" class="rule-toggle" ${rule.enabled ? "checked" : ""}
                 data-rule-id="${rule.id}" title="Enable/disable">
          <div class="rule-info" data-edit-rule="${rule.id}">
            <div class="rule-name">${escapeHtml(rule.name)}</div>
            <div class="rule-meta">${typeLabel} · ${rule.action} · ${rule.gracePeriodDays}d grace ${badges.join(" ")}</div>
          </div>
        </div>`;
    })
    .join("");

  // Toggle handlers
  container.querySelectorAll(".rule-toggle").forEach((cb) => {
    cb.addEventListener("change", async (e) => {
      e.stopPropagation();
      const ruleId = e.target.dataset.ruleId;
      const rule = currentRules.find((r) => r.id === ruleId);
      if (rule) {
        rule.enabled = e.target.checked;
        await saveRule(rule);
        await loadRules();
      }
    });
  });

  // Edit handlers
  container.querySelectorAll("[data-edit-rule]").forEach((el) => {
    el.addEventListener("click", () => {
      openRuleEditor(el.dataset.editRule);
    });
  });
}

document.getElementById("btnAddRule").addEventListener("click", () => {
  openRuleEditor(null);
});

document.getElementById("btnResetRules").addEventListener("click", async () => {
  if (confirm("Reset all rules to defaults? Custom rules will be lost.")) {
    await resetRulesToDefaults();
    await loadRules();
  }
});

// ── Rule Editor ─────────────────────────────────────────────────────────────

function openRuleEditor(ruleId) {
  editingRuleId = ruleId;
  const modal = document.getElementById("ruleEditor");
  const title = document.getElementById("ruleEditorTitle");

  if (ruleId) {
    const rule = currentRules.find((r) => r.id === ruleId);
    if (!rule) return;

    title.textContent = "Edit Rule";
    document.getElementById("ruleName").value = rule.name;
    document.getElementById("ruleEnabled").checked = rule.enabled;
    document.getElementById("ruleSenderPatterns").value = (rule.match.senderPatterns || []).join("\n");
    document.getElementById("ruleSubjectPatterns").value = (rule.match.subjectPatterns || []).join("\n");
    document.getElementById("ruleExpirationType").value = rule.expiration.type;
    document.getElementById("ruleTtlHours").value = rule.expiration.hours || 2;
    document.getElementById("ruleRegex").value = rule.expiration.pattern || "";
    document.getElementById("ruleLlmPrompt").value = rule.expiration.prompt || "";
    document.getElementById("ruleClassifyFolder").value = rule.classifyFolder || "Paper-Trail";
    document.getElementById("ruleClassifyCategory").value = rule.expiration.category || "receipt";
    document.getElementById("ruleAction").value = rule.action;
    document.getElementById("ruleGracePeriod").value = rule.gracePeriodDays;

    document.getElementById("btnDeleteRule").hidden = false;
  } else {
    title.textContent = "New Rule";
    document.getElementById("ruleName").value = "";
    document.getElementById("ruleEnabled").checked = true;
    document.getElementById("ruleSenderPatterns").value = "";
    document.getElementById("ruleSubjectPatterns").value = "";
    document.getElementById("ruleExpirationType").value = "ttl";
    document.getElementById("ruleTtlHours").value = 2;
    document.getElementById("ruleRegex").value = "";
    document.getElementById("ruleLlmPrompt").value = "";
    document.getElementById("ruleClassifyFolder").value = "Paper-Trail";
    document.getElementById("ruleClassifyCategory").value = "receipt";
    document.getElementById("ruleAction").value = "move";
    document.getElementById("ruleGracePeriod").value = 7;

    document.getElementById("btnDeleteRule").hidden = true;
  }

  updateExpirationFields();
  modal.hidden = false;
}

document.getElementById("ruleExpirationType").addEventListener("change", updateExpirationFields);

function updateExpirationFields() {
  const type = document.getElementById("ruleExpirationType").value;
  const isClassify = type === "classify" || type === "llm-classify";
  document.getElementById("fieldTtlHours").hidden = type !== "ttl";
  document.getElementById("fieldRegex").hidden = type !== "content-regex";
  document.getElementById("fieldLlmPrompt").hidden = type !== "llm" && type !== "llm-classify";
  document.getElementById("fieldClassifyFolder").hidden = !isClassify;
  document.getElementById("fieldClassifyCategory").hidden = type !== "llm-classify";
  document.getElementById("ruleActionLabel").textContent = isClassify ? "When matched" : "When expired";
  const actionSelect = document.getElementById("ruleAction");
  actionSelect.options[0].textContent = isClassify ? "Move to folder" : "Move to Expired folder";
  actionSelect.options[2].textContent = isClassify ? "Tag as classified" : "Tag as expired";
}

document.getElementById("btnSaveRule").addEventListener("click", async () => {
  const senderPatterns = document.getElementById("ruleSenderPatterns").value
    .split("\n").map((s) => s.trim()).filter(Boolean);
  const subjectPatterns = document.getElementById("ruleSubjectPatterns").value
    .split("\n").map((s) => s.trim()).filter(Boolean);

  const expType = document.getElementById("ruleExpirationType").value;
  const expiration = { type: expType };
  if (expType === "ttl") expiration.hours = parseFloat(document.getElementById("ruleTtlHours").value);
  if (expType === "content-regex") expiration.pattern = document.getElementById("ruleRegex").value;
  if (expType === "llm" || expType === "llm-classify") expiration.prompt = document.getElementById("ruleLlmPrompt").value || undefined;
  if (expType === "llm-classify") expiration.category = document.getElementById("ruleClassifyCategory").value;

  const classifyFolder = (expType === "classify" || expType === "llm-classify")
    ? (document.getElementById("ruleClassifyFolder").value || "Paper-Trail")
    : undefined;

  const rule = {
    id: editingRuleId || undefined,
    name: document.getElementById("ruleName").value || "Untitled Rule",
    enabled: document.getElementById("ruleEnabled").checked,
    priority: editingRuleId
      ? (currentRules.find((r) => r.id === editingRuleId)?.priority || 50)
      : (currentRules.length + 1) * 10,
    match: {
      folders: [],
      senderPatterns,
      subjectPatterns,
      headerMatch: expType === "header" ? { Expires: "*" } : {},
    },
    expiration,
    action: document.getElementById("ruleAction").value,
    destination: null,
    ...(classifyFolder && { classifyFolder }),
    tag: null,
    gracePeriodDays: parseInt(document.getElementById("ruleGracePeriod").value),
    builtin: editingRuleId
      ? (currentRules.find((r) => r.id === editingRuleId)?.builtin || false)
      : false,
  };

  await saveRule(rule);
  document.getElementById("ruleEditor").hidden = true;
  await loadRules();
});

document.getElementById("btnDeleteRule").addEventListener("click", async () => {
  if (editingRuleId && confirm("Delete this rule?")) {
    await deleteRule(editingRuleId);
    document.getElementById("ruleEditor").hidden = true;
    await loadRules();
  }
});

document.getElementById("btnCloseEditor").addEventListener("click", () => {
  document.getElementById("ruleEditor").hidden = true;
});

document.getElementById("btnCancelRule").addEventListener("click", () => {
  document.getElementById("ruleEditor").hidden = true;
});

// ── LLM Tab ─────────────────────────────────────────────────────────────────

async function loadLlm() {
  currentSettings = await messenger.runtime.sendMessage({ type: "getSettings" });

  document.getElementById("llmProvider").value = currentSettings.llmProvider;
  document.getElementById("geminiApiKey").value = currentSettings.geminiApiKey;
  document.getElementById("geminiModel").value = currentSettings.geminiModel;
  document.getElementById("ollamaEndpoint").value = currentSettings.ollamaEndpoint;
  document.getElementById("ollamaModel").value = currentSettings.ollamaModel;
  document.getElementById("llmMetadataOnly").checked = currentSettings.llmMetadataOnly;
  document.getElementById("llmMaxSnippet").value = currentSettings.llmMaxSnippetLength;
  document.getElementById("llmConfidence").value = currentSettings.llmConfidenceThreshold;
  document.getElementById("llmConfidenceValue").textContent = currentSettings.llmConfidenceThreshold;

  updateProviderVisibility();
}

document.getElementById("llmProvider").addEventListener("change", updateProviderVisibility);

function updateProviderVisibility() {
  const provider = document.getElementById("llmProvider").value;
  document.getElementById("geminiSettings").hidden = provider !== "gemini";
  document.getElementById("ollamaSettings").hidden = provider !== "ollama";
}

document.getElementById("llmConfidence").addEventListener("input", (e) => {
  document.getElementById("llmConfidenceValue").textContent = e.target.value;
});

document.getElementById("btnSaveLlm").addEventListener("click", async () => {
  try {
    await messenger.runtime.sendMessage({
      type: "updateSettings",
      settings: {
        llmProvider: document.getElementById("llmProvider").value,
        geminiApiKey: document.getElementById("geminiApiKey").value,
        geminiModel: document.getElementById("geminiModel").value,
        ollamaEndpoint: document.getElementById("ollamaEndpoint").value,
        ollamaModel: document.getElementById("ollamaModel").value,
        llmMetadataOnly: document.getElementById("llmMetadataOnly").checked,
        llmMaxSnippetLength: parseInt(document.getElementById("llmMaxSnippet").value),
        llmConfidenceThreshold: parseFloat(document.getElementById("llmConfidence").value),
      },
    });
    showSaveStatus("llmSaveStatus");
  } catch (e) {
    console.error("Failed to save LLM settings:", e);
    showSaveStatus("llmSaveStatus", "Save failed");
  }
});

document.getElementById("btnClearLlmCache").addEventListener("click", async () => {
  if (confirm("Clear all cached LLM verdicts? Next scan will re-evaluate all messages.")) {
    const { clearLlmCache } = await import("../rules/storage.js");
    await clearLlmCache();
    const btn = document.getElementById("btnClearLlmCache");
    btn.textContent = "Cleared!";
    setTimeout(() => { btn.textContent = "Clear LLM Cache"; }, 2000);
  }
});

document.getElementById("btnTestLlm").addEventListener("click", async () => {
  const btn = document.getElementById("btnTestLlm");
  btn.disabled = true;
  btn.textContent = "Testing...";

  try {
    const testSettings = {
      llmProvider: document.getElementById("llmProvider").value,
      geminiApiKey: document.getElementById("geminiApiKey").value,
      geminiModel: document.getElementById("geminiModel").value,
      ollamaEndpoint: document.getElementById("ollamaEndpoint").value,
      ollamaModel: document.getElementById("ollamaModel").value,
    };

    const result = await messenger.runtime.sendMessage({
      type: "testLlmConnection",
      settings: testSettings,
    });

    btn.disabled = false;
    if (result.success) {
      btn.textContent = "✓ Connected!";
      btn.style.color = "var(--accent-green)";
    } else {
      btn.textContent = `✗ Failed: ${result.error}`;
      btn.style.color = "var(--danger)";
    }
  } catch (e) {
    console.error("LLM connection test failed:", e);
    btn.disabled = false;
    btn.textContent = `✗ Error: ${e.message || "unknown"}`;
    btn.style.color = "var(--danger)";
  }

  setTimeout(() => {
    btn.textContent = "Test Connection";
    btn.style.color = "";
  }, 4000);
});

// ── Folders Tab ─────────────────────────────────────────────────────────────

let selectedFolderIds = new Set();

async function loadFolders() {
  currentSettings = await messenger.runtime.sendMessage({ type: "getSettings" });
  selectedFolderIds = new Set(currentSettings.scannedFolderIds || []);

  const accounts = await messenger.runtime.sendMessage({ type: "getAccounts" });
  const container = document.getElementById("folderTree");

  if (!accounts || accounts.length === 0) {
    container.innerHTML = '<div class="empty-state">No mail accounts found.</div>';
    return;
  }

  container.innerHTML = accounts
    .map(
      (account) => `
    <div class="folder-account" data-account-id="${account.id}">
      <div class="folder-account-header">
        <div class="folder-account-name">${escapeHtml(account.name)} (${account.type})</div>
        <div class="folder-account-actions">
          <button class="btn-select-all" data-account-id="${account.id}">Select all</button>
          <button class="btn-select-none" data-account-id="${account.id}">Select none</button>
        </div>
      </div>
      ${account.folders
        .map((folder) => {
          const depth = (folder.path.match(/\//g) || []).length;
          const indent = depth > 0 ? `folder-indent-${Math.min(depth, 2)}` : "";
          const icon = folderIcon(folder.type);
          return `
          <div class="folder-item ${indent}">
            <label>
              <input type="checkbox" class="folder-checkbox"
                     data-folder-id="${folder.id}"
                     data-account-id="${account.id}"
                     ${selectedFolderIds.has(folder.id) ? "checked" : ""}>
              ${icon} ${escapeHtml(folder.name)}
            </label>
          </div>`;
        })
        .join("")}
    </div>`
    )
    .join("");

  container.querySelectorAll(".folder-checkbox").forEach((cb) => {
    cb.addEventListener("change", (e) => {
      if (e.target.checked) {
        selectedFolderIds.add(e.target.dataset.folderId);
      } else {
        selectedFolderIds.delete(e.target.dataset.folderId);
      }
      updateFolderCallout();
    });
  });

  container.querySelectorAll(".btn-select-all").forEach((btn) => {
    btn.addEventListener("click", () => {
      const accountId = btn.dataset.accountId;
      container.querySelectorAll(`.folder-checkbox[data-account-id="${accountId}"]`).forEach((cb) => {
        cb.checked = true;
        selectedFolderIds.add(cb.dataset.folderId);
      });
      updateFolderCallout();
    });
  });

  container.querySelectorAll(".btn-select-none").forEach((btn) => {
    btn.addEventListener("click", () => {
      const accountId = btn.dataset.accountId;
      container.querySelectorAll(`.folder-checkbox[data-account-id="${accountId}"]`).forEach((cb) => {
        cb.checked = false;
        selectedFolderIds.delete(cb.dataset.folderId);
      });
      updateFolderCallout();
    });
  });

  updateFolderCallout();
}

function updateFolderCallout() {
  const callout = document.getElementById("folderCallout");
  callout.hidden = selectedFolderIds.size > 0;
}

function folderIcon(type) {
  const icons = {
    inbox: "📥",
    sent: "📤",
    trash: "🗑️",
    junk: "🚫",
    drafts: "📝",
    archives: "📦",
  };
  return icons[type] || "📁";
}

document.getElementById("btnSaveFolders").addEventListener("click", async () => {
  try {
    await messenger.runtime.sendMessage({
      type: "updateSettings",
      settings: {
        scannedFolderIds: [...selectedFolderIds],
      },
    });
    showSaveStatus("folderSaveStatus");
  } catch (e) {
    console.error("Failed to save folder settings:", e);
    showSaveStatus("folderSaveStatus", "Save failed");
  }
});

// ── Activity Tab ────────────────────────────────────────────────────────────

const ACTION_ICONS = {
  moved: "📦",
  classified: "📂",
  deleted: "🗑️",
  tagged: "🏷️",
  grace_deleted: "💀",
  error: "⚠️",
};

async function loadActivity() {
  const log = await messenger.runtime.sendMessage({
    type: "getActivityLog",
    limit: 100,
  });

  const container = document.getElementById("activityTable");

  if (!log || log.length === 0) {
    container.innerHTML = '<div class="empty-state">No activity recorded yet. Expired emails will appear here.</div>';
    return;
  }

  container.innerHTML = log
    .map(
      (entry) => `
    <div class="activity-row">
      <span class="icon">${ACTION_ICONS[entry.type] || "•"}</span>
      <span class="subject" title="${escapeHtml(entry.subject || "")}">${escapeHtml(entry.subject || "Unknown")}</span>
      <span class="rule" title="${escapeHtml(entry.reason || "")}">${escapeHtml(entry.rule || "—")}</span>
      <span class="time">${formatTime(entry.timestamp)}</span>
    </div>`
    )
    .join("");
}

document.getElementById("btnClearLog").addEventListener("click", async () => {
  if (confirm("Clear the activity log?")) {
    // Send a message to background to clear — we could also import storage directly
    // but keeping it consistent with the message pattern
    const { clearActivityLog } = await import("../rules/storage.js");
    await clearActivityLog();
    await loadActivity();
  }
});

// ── Utilities ───────────────────────────────────────────────────────────────

function escapeHtml(str) {
  const div = document.createElement("div");
  div.textContent = str;
  return div.innerHTML;
}

function showSaveStatus(elementId, message = "✓ Saved") {
  const el = document.getElementById(elementId);
  el.textContent = message;
  setTimeout(() => { el.textContent = ""; }, 2000);
}

function formatTime(isoString) {
  if (!isoString) return "—";
  const d = new Date(isoString);
  const now = new Date();
  const diffMs = now - d;
  const diffMin = Math.round(diffMs / 60000);

  if (diffMin < 1) return "just now";
  if (diffMin < 60) return `${diffMin}m ago`;
  if (diffMin < 1440) return `${Math.round(diffMin / 60)}h ago`;
  return d.toLocaleDateString();
}

// ── Initialize ──────────────────────────────────────────────────────────────

async function init() {
  await loadGeneral();
  await loadRules();
  await loadLlm();
}

init().catch((e) => console.error("Options init failed:", e));
