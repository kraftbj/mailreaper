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
    // loadStatus polling will update the UI and re-enable when done
    await loadStatus();
  } catch (e) {
    console.error("Scan trigger failed:", e);
  }

  btn.disabled = false;
  btn.textContent = "Scan Now";
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

// ── Init ────────────────────────────────────────────────────────────────────

loadStatus();

// Refresh every 10 seconds while popup is open
setInterval(loadStatus, 10000);
