/**
 * Popup script — communicates with background.js to show status.
 */

const ACTION_ICONS = {
  moved: "📦",
  deleted: "🗑️",
  tagged: "🏷️",
  grace_deleted: "💀",
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
    document.getElementById("statExpired").textContent =
      status.lastScanResults?.expired ?? "—";
    document.getElementById("statProcessed").textContent =
      status.lastScanResults?.processed ?? "—";
    document.getElementById("statErrors").textContent =
      status.lastScanResults?.errors ?? "—";

    // Last scan
    if (status.lastScanTime) {
      document.getElementById("lastScan").textContent = formatRelativeTime(
        new Date(status.lastScanTime)
      );
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

    // Activity log
    renderActivity(activityLog);
  } catch (e) {
    console.error("Failed to load status:", e);
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
      (entry) => `
    <div class="activity-item">
      <span class="activity-icon">${ACTION_ICONS[entry.type] || "•"}</span>
      <span class="activity-text" title="${escapeHtml(entry.subject || "")}">${escapeHtml(truncate(entry.subject || "Unknown", 30))}</span>
      <span class="activity-time">${formatRelativeTime(new Date(entry.timestamp))}</span>
    </div>
  `
    )
    .join("");
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

// ── Button Handlers ─────────────────────────────────────────────────────────

document.getElementById("btnScan").addEventListener("click", async () => {
  const btn = document.getElementById("btnScan");
  btn.disabled = true;
  btn.textContent = "Scanning...";

  try {
    await messenger.runtime.sendMessage({ type: "triggerScan" });
    // Wait a moment then refresh status
    setTimeout(async () => {
      await loadStatus();
      btn.disabled = false;
      btn.textContent = "Scan Now";
    }, 3000);
  } catch (e) {
    btn.disabled = false;
    btn.textContent = "Scan Now";
    console.error("Scan trigger failed:", e);
  }
});

document.getElementById("btnSettings").addEventListener("click", () => {
  messenger.runtime.openOptionsPage();
  window.close();
});

// ── Init ────────────────────────────────────────────────────────────────────

loadStatus();

// Refresh every 10 seconds while popup is open
setInterval(loadStatus, 10000);
