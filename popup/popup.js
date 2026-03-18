/**
 * Popup script — communicates with background.js to show status.
 */

const ACTION_ICONS = {
  moved: "📦",
  classified: "📂",
  deleted: "🗑️",
  tagged: "🏷️",
  grace_deleted: "💀",
  manual_expiry_set: "⏰",
  error: "⚠️",
};

async function loadStatus() {
  try {
    const [status, settings, activityLog] = await Promise.all([
      messenger.runtime.sendMessage({ type: "getStatus" }),
      messenger.runtime.sendMessage({ type: "getSettings" }),
      messenger.runtime.sendMessage({ type: "getActivityLog", limit: 5 }),
    ]);

    // Status dot
    const dot = document.getElementById("statusDot");
    if (status.scanInProgress) {
      dot.className = "status-dot scanning";
    } else if (status.scanEnabled) {
      dot.className = "status-dot active";
    } else {
      dot.className = "status-dot inactive";
    }

    // Stats
    const results = status.lastScanResults || {};
    document.getElementById("statExpired").textContent = results.expired ?? "—";
    document.getElementById("statProcessed").textContent = results.processed ?? "—";
    document.getElementById("statErrors").textContent = results.errors ?? "—";

    // Progress bar
    const progressEl = document.getElementById("scanProgress");
    if (status.scanInProgress && results.totalCandidates > 0) {
      progressEl.style.display = "block";
      const pct = Math.round((results.processed / results.totalCandidates) * 100);
      document.getElementById("progressBar").style.width = pct + "%";
      const skippedText = results.skipped > 0 ? ` (${results.skipped} cached)` : "";
      document.getElementById("progressText").textContent =
        `${results.processed} / ${results.totalCandidates}${skippedText}`;
    } else {
      progressEl.style.display = "none";
    }

    // Last scan
    if (status.lastScanTime) {
      const scanDate = new Date(status.lastScanTime);
      if (!isNaN(scanDate.getTime())) {
        document.getElementById("lastScan").textContent = formatRelativeTime(scanDate);
      }
    } else if (status.scanInProgress) {
      document.getElementById("lastScan").textContent = "In progress...";
    }

    // Next scan
    const alarm = await messenger.alarms.get("mailreaper-scan");
    if (alarm && alarm.scheduledTime) {
      document.getElementById("nextScan").textContent = formatRelativeTime(
        new Date(alarm.scheduledTime),
        true
      );
    }

    // LLM provider
    const providerNames = { none: "None", gemini: "Gemini", ollama: "Ollama" };
    document.getElementById("llmProvider").textContent =
      providerNames[settings.llmProvider] || "None";

    // Error display
    const errorRow = document.getElementById("errorRow");
    if (status.lastScanError) {
      errorRow.style.display = "";
      document.getElementById("lastError").textContent = status.lastScanError;
    } else {
      errorRow.style.display = "none";
    }

    // Poll faster while scanning
    if (status.scanInProgress && !fastPolling) {
      fastPolling = setInterval(loadStatus, 1500);
    } else if (!status.scanInProgress && fastPolling) {
      clearInterval(fastPolling);
      fastPolling = null;
    }

    // Activity log
    renderActivity(activityLog);
  } catch (e) {
    console.error("Failed to load status:", e);
    const dot = document.getElementById("statusDot");
    if (dot) dot.className = "status-dot error";
    const lastScan = document.getElementById("lastScan");
    if (lastScan) lastScan.textContent = "Could not connect to MailReaper";
  }
}

function renderActivity(entries) {
  const container = document.getElementById("activityList");

  if (!entries || entries.length === 0) {
    container.innerHTML = '<div class="empty-state">No activity yet</div>';
    return;
  }

  container.innerHTML = entries
    .map(
      (entry, i) => `
    <div class="activity-item${entry.undone ? " activity-undone" : ""}">
      <span class="activity-icon">${ACTION_ICONS[entry.type] || "•"}</span>
      <span class="activity-text" title="${escapeHtml(entry.subject || "")}">${escapeHtml(truncate(entry.subject || "Unknown", 30))}</span>
      ${entry.undoable ? `<button class="undo-btn" data-undo-id="${escapeAttr(entry.id)}">undo</button>` : ""}
      ${entry.undone ? `<span class="activity-time">undone</span>` : `<span class="activity-time">${formatRelativeTime(new Date(entry.timestamp))}</span>`}
    </div>
  `
    )
    .join("");

  // Attach undo click handlers
  container.querySelectorAll(".undo-btn").forEach((btn) => {
    btn.addEventListener("click", async (e) => {
      e.preventDefault();
      const logId = btn.dataset.undoId;
      btn.disabled = true;
      btn.textContent = "...";
      try {
        const result = await messenger.runtime.sendMessage({
          type: "undoManualAction",
          logId,
        });
        if (result?.success) {
          await loadStatus();
        } else {
          btn.textContent = result?.error || "failed";
        }
      } catch (e) {
        console.warn("Undo failed:", e);
        btn.textContent = `failed: ${e.message || "unknown error"}`;
      }
    });
  });
}

function formatRelativeTime(date, future = false) {
  const now = Date.now();
  const diffMs = future ? date.getTime() - now : now - date.getTime();
  const diffMin = Math.round(diffMs / 60000);

  if (diffMin < 1) return future ? "now" : "just now";
  if (diffMin < 60) return `${diffMin}m ${future ? "" : "ago"}`.trim();

  const diffHr = Math.round(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ${future ? "" : "ago"}`.trim();

  const diffDays = Math.round(diffHr / 24);
  return `${diffDays}d ${future ? "" : "ago"}`.trim();
}

function truncate(str, len) {
  return str.length > len ? str.substring(0, len - 1) + "…" : str;
}

function escapeHtml(str) {
  const div = document.createElement("div");
  div.textContent = str;
  return div.innerHTML;
}

function escapeAttr(str) {
  return String(str).replace(/&/g, "&amp;").replace(/"/g, "&quot;");
}

// ── Button Handlers ─────────────────────────────────────────────────────────

document.getElementById("btnScan").addEventListener("click", async () => {
  const btn = document.getElementById("btnScan");
  btn.disabled = true;
  btn.textContent = "Scanning...";

  try {
    await messenger.runtime.sendMessage({ type: "triggerScan" });
  } catch (e) {
    console.error("Scan trigger failed:", e);
    btn.textContent = "Scan failed";
  }

  // Keep button disabled briefly to prevent double-clicks; loadStatus
  // polling will reflect scanInProgress and update the button accordingly
  setTimeout(async () => {
    await loadStatus();
    btn.disabled = false;
    btn.textContent = "Scan Now";
  }, 3000);
});

document.getElementById("btnDiagnose").addEventListener("click", async () => {
  const btn = document.getElementById("btnDiagnose");
  const output = document.getElementById("diagOutput");
  btn.disabled = true;
  btn.textContent = "Running...";
  output.style.display = "block";
  output.textContent = "Running diagnostic...";

  try {
    const report = await messenger.runtime.sendMessage({ type: "runDiagnostic" });
    output.textContent = JSON.stringify(report, null, 2);
  } catch (e) {
    output.textContent = "Diagnostic failed: " + e.message;
  }

  btn.disabled = false;
  btn.textContent = "Diagnose";
});

document.getElementById("btnSettings").addEventListener("click", () => {
  messenger.runtime.openOptionsPage();
  window.close();
});

let fastPolling = null;
let selectedMessageId = null;

// ── Selected Message ─────────────────────────────────────────────────────────

async function loadSelectedMessage() {
  const section = document.getElementById("selectedMessage");
  const statusEl = document.getElementById("selectedStatus");
  const infoEl = document.getElementById("messageInfo");
  statusEl.style.display = "none";
  infoEl.style.display = "none";

  try {
    // Try message display first (works when viewing a message in its own tab)
    // then fall back to mailTabs selection (message list)
    let msg = null;
    try {
      const tabs = await messenger.tabs.query({ active: true, currentWindow: true });
      if (tabs.length > 0) {
        const displayed = await messenger.messageDisplay.getDisplayedMessage(tabs[0].id);
        if (displayed) msg = displayed;
      }
    } catch (e) {
      console.warn("Could not get displayed message:", e);
    }

    if (!msg) {
      const result = await messenger.mailTabs.getSelectedMessages();
      if (result && result.messages && result.messages.length === 1) {
        msg = result.messages[0];
      }
    }

    if (msg) {
      selectedMessageId = msg.id;
      const subjectEl = document.getElementById("selectedSubject");
      const display = truncate(msg.subject || "(no subject)", 45);
      subjectEl.textContent = display;
      subjectEl.title = msg.subject || "";
      section.style.display = "block";

      // Fetch what MailReaper knows about this message
      try {
        const info = await messenger.runtime.sendMessage({
          type: "getMessageInfo",
          messageId: msg.id,
        });
        if (info && info.status && info.status.length > 0) {
          infoEl.innerHTML = info.status.map((s) =>
            `<div class="msg-info-item"><span class="info-icon">${s.icon}</span><span class="info-text" title="${escapeHtml(s.text)}">${escapeHtml(s.text)}</span></div>`
          ).join("");
          infoEl.style.display = "block";
        }
      } catch (e) {
        console.warn("Message info lookup failed:", e);
      }
    } else {
      selectedMessageId = null;
      section.style.display = "none";
    }
  } catch (e) {
    console.error("Failed to load selected message:", e);
    selectedMessageId = null;
    section.style.display = "none";
  }
}

function showSelectedStatus(text) {
  const el = document.getElementById("selectedStatus");
  el.textContent = text;
  el.style.display = "block";
  setTimeout(() => { el.style.display = "none"; }, 4000);
}

document.getElementById("btnMarkReceipt").addEventListener("click", async () => {
  if (!selectedMessageId) return;
  const btn = document.getElementById("btnMarkReceipt");
  btn.disabled = true;
  try {
    const result = await messenger.runtime.sendMessage({
      type: "markAsReceipt",
      messageId: selectedMessageId,
    });
    if (result?.success) {
      showSelectedStatus("Marked as receipt and moved to Paper-Trail");
      await loadStatus();
    } else {
      showSelectedStatus("Failed: " + (result?.error || "unknown error"));
    }
  } catch (e) {
    showSelectedStatus("Error: " + e.message);
  }
  btn.disabled = false;
});

document.getElementById("btnMarkExpired").addEventListener("click", async () => {
  if (!selectedMessageId) return;
  const btn = document.getElementById("btnMarkExpired");
  btn.disabled = true;
  try {
    const result = await messenger.runtime.sendMessage({
      type: "markAsExpired",
      messageId: selectedMessageId,
    });
    if (result?.success) {
      showSelectedStatus("Marked as expired and moved to Expired folder");
      await loadStatus();
    } else {
      showSelectedStatus("Failed: " + (result?.error || "unknown error"));
    }
  } catch (e) {
    showSelectedStatus("Error: " + e.message);
  }
  btn.disabled = false;
});

document.getElementById("btnSetExpiry").addEventListener("click", async () => {
  if (!selectedMessageId) return;
  const btn = document.getElementById("btnSetExpiry");
  const hours = parseInt(document.getElementById("expiryPreset").value, 10);
  btn.disabled = true;
  try {
    const result = await messenger.runtime.sendMessage({
      type: "setManualExpiry",
      messageId: selectedMessageId,
      hours,
    });
    if (result?.success) {
      const label = hours >= 24 ? `${hours / 24} day(s)` : `${hours} hour(s)`;
      showSelectedStatus(`Expires in ${label}`);
      await loadStatus();
    } else {
      showSelectedStatus("Failed: " + (result?.error || "unknown error"));
    }
  } catch (e) {
    showSelectedStatus("Error: " + e.message);
  }
  btn.disabled = false;
});

// ── Init ────────────────────────────────────────────────────────────────────

loadStatus().catch((e) => console.error("[MailReaper] Popup loadStatus failed:", e));
loadSelectedMessage().catch((e) => console.error("[MailReaper] Popup loadSelectedMessage failed:", e));

// Refresh every 10 seconds while popup is open
setInterval(loadStatus, 10000);
