/**
 * MailReaper Background Script
 *
 * Orchestrates the scan cycle:
 *   1. Timer fires (or manual trigger)
 *   2. Walk configured folders, collecting candidate messages
 *   3. Evaluate each candidate against rules
 *   4. Execute actions on expired messages
 *   5. Run grace period cleanup
 */

import {
  initializeStorage, getSettings, getRules, getActivityLog, logActivity, updateSettings,
  getManualOverrides, setManualOverride, removeManualOverride,
  addTrainingExample, getTrainingExamples, removeTrainingExampleBySubject, saveRule,
  getCachedVerdict, removeCachedVerdict, STORAGE_KEYS,
} from "./rules/storage.js";
import { evaluateMessage } from "./rules/engine.js";
import { executeAction, cleanupGracePeriod } from "./actions/executor.js";
import { testLlmConnection, generateRuleFromExamples } from "./llm/adapter.js";

const ALARM_NAME = "mailreaper-scan";
const GRACE_ALARM_NAME = "mailreaper-grace-cleanup";

let scanInProgress = false;
let lastScanTime = null;
let lastScanResults = { processed: 0, expired: 0, errors: 0 };
let lastScanError = null;

// Persist scan state so it survives service worker suspension
const SCAN_STATE_KEY = "mailreaper_scan_state";

async function persistScanState() {
  try {
    await messenger.storage.local.set({
      [SCAN_STATE_KEY]: { lastScanTime, lastScanResults, lastScanError },
    });
  } catch (e) {
    console.error("[MailReaper] Failed to persist scan state:", e);
  }
}

async function restoreScanState() {
  try {
    const { [SCAN_STATE_KEY]: state } = await messenger.storage.local.get(SCAN_STATE_KEY);
    if (state) {
      lastScanTime = state.lastScanTime;
      lastScanResults = state.lastScanResults || { processed: 0, expired: 0, errors: 0 };
      lastScanError = state.lastScanError;
    }
  } catch (e) {
    console.warn("[MailReaper] Failed to restore scan state, continuing with defaults:", e);
  }
}

// Tracks messages already evaluated with no match.
// Keyed by message ID, value is { fingerprint, trace } where fingerprint is
// the rules fingerprint at evaluation time and trace lists rules checked.
// When rules change, the cache is cleared so messages are re-evaluated.
let evaluatedNoMatch = new Map();
let currentRulesFingerprint = null;

// ── Initialization ──────────────────────────────────────────────────────────

async function init() {
  console.log("[MailReaper] Initializing...");

  await initializeStorage();
  await restoreScanState();
  const settings = await getSettings();

  // Set up scan alarm
  if (settings.scanEnabled) {
    await setupAlarm(settings.scanIntervalMinutes);
  }

  // Set up grace period cleanup alarm (runs every 6 hours)
  await messenger.alarms.create(GRACE_ALARM_NAME, {
    periodInMinutes: 360,
    delayInMinutes: 10, // First run 10 minutes after startup
  });

  // Set up context menus for message actions
  await setupContextMenus();

  console.log("[MailReaper] Initialized. Scan interval:", settings.scanIntervalMinutes, "minutes");
}

async function setupAlarm(intervalMinutes) {
  // Clear existing alarm
  await messenger.alarms.clear(ALARM_NAME);

  await messenger.alarms.create(ALARM_NAME, {
    periodInMinutes: intervalMinutes,
    delayInMinutes: 2, // First scan 2 minutes after startup
  });
}

// ── Alarm Handler ───────────────────────────────────────────────────────────

messenger.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name === ALARM_NAME) {
    try {
      await runScan();
    } catch (e) {
      console.error("[MailReaper] Scan alarm handler failed:", e);
    }
  } else if (alarm.name === GRACE_ALARM_NAME) {
    try {
      await cleanupGracePeriod();
    } catch (e) {
      console.error("[MailReaper] Grace period cleanup failed:", e);
    }
  }
});

// ── Context Menus ────────────────────────────────────────────────────────────

async function setupContextMenus() {
  await messenger.menus.removeAll();

  messenger.menus.create({
    id: "mailreaper-mark-receipt",
    title: "MailReaper: Mark as Receipt",
    contexts: ["message_list"],
  });

  messenger.menus.create({
    id: "mailreaper-mark-expired",
    title: "MailReaper: Mark as Expired",
    contexts: ["message_list"],
  });

  messenger.menus.create({
    id: "mailreaper-separator",
    type: "separator",
    contexts: ["message_list"],
  });

  messenger.menus.create({
    id: "mailreaper-expire-1h",
    title: "MailReaper: Expire in 1 hour",
    contexts: ["message_list"],
  });

  messenger.menus.create({
    id: "mailreaper-expire-1d",
    title: "MailReaper: Expire in 1 day",
    contexts: ["message_list"],
  });

  messenger.menus.create({
    id: "mailreaper-expire-7d",
    title: "MailReaper: Expire in 7 days",
    contexts: ["message_list"],
  });
}

messenger.menus.onClicked.addListener(async (info) => {
  // Get the message ID from the menu click
  // info.selectedMessages contains the messages when context is message_list
  const messages = info.selectedMessages?.messages;
  if (!messages || messages.length === 0) return;

  // Act on each selected message
  for (const msg of messages) {
    try {
      switch (info.menuItemId) {
        case "mailreaper-mark-receipt":
          await handleMarkAsReceipt(msg.id);
          break;
        case "mailreaper-mark-expired":
          await handleMarkAsExpired(msg.id);
          break;
        case "mailreaper-expire-1h":
          await handleSetManualExpiry(msg.id, 1);
          break;
        case "mailreaper-expire-1d":
          await handleSetManualExpiry(msg.id, 24);
          break;
        case "mailreaper-expire-7d":
          await handleSetManualExpiry(msg.id, 168);
          break;
      }
    } catch (e) {
      console.error(`[MailReaper] Context menu action failed for message ${msg.id}:`, e);
    }
  }
});

// ── Core Scan Logic ─────────────────────────────────────────────────────────

async function runScan() {
  if (scanInProgress) {
    console.log("[MailReaper] Scan already in progress, skipping");
    return;
  }

  scanInProgress = true;
  lastScanError = null;
  const startTime = Date.now();
  let processed = 0;
  let expired = 0;
  let errors = 0;
  let totalCandidates = 0;
  let skipped = 0;

  // Update results in real-time so the popup always reflects progress
  function updateProgress() {
    lastScanTime = new Date().toISOString();
    lastScanResults = { processed, expired, errors, totalCandidates, skipped };
  }

  try {
    const settings = await getSettings();

    if (!settings.scanEnabled) {
      lastScanError = "Scanning is disabled in settings";
      console.log("[MailReaper] Scanning is disabled");
      updateProgress();
      return;
    }

    const folders = await resolveScanFolders(settings);

    if (folders.length === 0) {
      lastScanError = "No folders resolved for scanning";
      console.log("[MailReaper] No folders configured for scanning");
      updateProgress();
      return;
    }

    // Compute rules fingerprint for cache validity
    const rules = await getRules();
    const fingerprint = computeRulesFingerprint(rules);
    if (currentRulesFingerprint !== null && currentRulesFingerprint !== fingerprint) {
      evaluatedNoMatch.clear();
      console.log("[MailReaper] Rules fingerprint changed, cleared evaluation cache");
    }
    currentRulesFingerprint = fingerprint;

    // Load manual overrides once for the entire scan
    const overrides = await getManualOverrides();

    const cachedCount = evaluatedNoMatch.size;
    console.log(`[MailReaper] Starting scan of ${folders.length} folder(s):`, folders.map((f) => f.path || f.name));
    console.log(`[MailReaper] Skipping ${cachedCount} previously evaluated messages`);

    const minAgeMs = settings.minMessageAgeMinutes * 60 * 1000;
    const cutoffDate = new Date(Date.now() - minAgeMs);
    const now = new Date();

    for (const folder of folders) {
      if (processed >= settings.maxMessagesPerScan) break;

      try {
        const candidates = await getCandidateMessages(folder, cutoffDate, settings);
        totalCandidates += candidates.length;
        updateProgress();

        for (const message of candidates) {
          if (processed >= settings.maxMessagesPerScan) break;

          try {
            // Check manual overrides (user-set expiry takes priority)
            if (message.headerMessageId && overrides[message.headerMessageId]) {
              const override = overrides[message.headerMessageId];
              const expiresAt = new Date(override.expiresAt);
              if (now > expiresAt) {
                const verdict = {
                  expired: true,
                  rule: { name: "Manual expiry", action: "move", id: "manual-override" },
                  expiresAt: override.expiresAt,
                  reason: "Manually set expiry",
                  confidence: 1.0,
                };
                await executeAction(message, verdict);
                await removeManualOverride(message.headerMessageId);
                expired++;
              }
              continue;
            }

            // Skip messages we've already evaluated with no match
            // under the current rules fingerprint
            if (evaluatedNoMatch.get(message.id)?.fingerprint === fingerprint) {
              skipped++;
              continue;
            }

            processed++;
            updateProgress();

            // Evaluate against rules — full message data is fetched lazily
            // only when a rule actually needs it (e.g. Expires header, LLM)
            const verdict = await evaluateMessage(
              message,
              () => messenger.messages.getFull(message.id),
              async () => {
                try {
                  const parts = await messenger.messages.listInlineTextParts(message.id);
                  return parts.map((p) => p.content).join("\n");
                } catch (e) {
                  console.warn(`[MailReaper] Failed to get body text for message ${message.id}:`, e);
                  return "";
                }
              }
            );

            if (verdict && (verdict.expired || verdict.classified)) {
              await executeAction(message, verdict);
              expired++;
            } else {
              const trace = verdict?.trace || [];
              evaluatedNoMatch.set(message.id, { fingerprint, trace });
            }
          } catch (e) {
            errors++;
            console.error(`[MailReaper] Error processing message ${message.id}:`, e);
          }
        }
      } catch (e) {
        errors++;
        console.error(`[MailReaper] Error scanning folder ${folder.id}:`, e);
      }
    }

    const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);
    console.log(
      `[MailReaper] Scan complete in ${elapsed}s: ${processed} processed, ${expired} expired, ${errors} errors`
    );

    // Send notification if configured and any expired
    if (settings.notifyOnExpiration && expired > 0) {
      try {
        await messenger.notifications.create(`mailreaper-scan-${Date.now()}`, {
          type: "basic",
          title: "MailReaper",
          message: `Cleaned up ${expired} expired email${expired !== 1 ? "s" : ""}.`,
        });
      } catch (notifErr) {
        console.warn("[MailReaper] Notification failed:", notifErr);
      }
    }
  } catch (e) {
    lastScanError = e.message;
    console.error("[MailReaper] Scan failed:", e);
  } finally {
    scanInProgress = false;
    updateProgress();
    await persistScanState();
  }
}

/**
 * Resolve which folders to scan based on settings.
 */
async function resolveScanFolders(settings) {
  // Build a map of all folders across all accounts
  const accounts = await messenger.accounts.list(true);
  const allFolders = [];

  for (const account of accounts) {
    collectFolders(account.rootFolder?.subFolders || [], allFolders);
  }

  if (settings.scannedFolderIds && settings.scannedFolderIds.length > 0) {
    // User has configured specific folders — match by ID
    const idSet = new Set(settings.scannedFolderIds);
    const matched = allFolders.filter((f) => idSet.has(f.id));
    if (matched.length > 0) return matched;
    // Configured folders vanished — don't fall back to inbox, that could
    // be destructive. Surface the error and scan nothing.
    lastScanError = "Configured scan folders not found. Check your folder settings.";
    console.warn("[MailReaper] Configured folders not found, scanning nothing");
    return [];
  }

  // No folders configured — default to all inbox folders
  return allFolders.filter((f) => f.type === "inbox");
}

/**
 * Recursively collect all folders into a flat array.
 */
function collectFolders(folders, result) {
  for (const folder of folders) {
    result.push(folder);
    if (folder.subFolders && folder.subFolders.length > 0) {
      collectFolders(folder.subFolders, result);
    }
  }
}

/**
 * Get candidate messages from a folder that are old enough to evaluate.
 */
async function getCandidateMessages(folder, cutoffDate, settings) {
  const messages = [];

  try {
    const list = await messenger.messages.query({
      folderId: folder.id,
      toDate: cutoffDate,
    });

    messages.push(...(list.messages || []));

    // Get additional pages if available
    let page = list;
    while (page.id && messages.length < settings.maxMessagesPerScan) {
      page = await messenger.messages.continueList(page.id);
      messages.push(...(page.messages || []));
    }
  } catch (e) {
    console.error(`[MailReaper] Query failed for folder ${folder.path}:`, e);
    throw e;
  }

  console.log(`[MailReaper] Found ${messages.length} candidate messages in ${folder.name}`);
  return messages;
}

// ── Message Handler for Runtime Communication ───────────────────────────────

messenger.runtime.onMessage.addListener(async (message, sender) => {
  switch (message.type) {
    case "getStatus":
      return {
        scanInProgress,
        lastScanTime,
        lastScanResults,
        lastScanError,
        scanEnabled: (await getSettings()).scanEnabled,
      };

    case "triggerScan":
      // Don't await — let it run in background
      runScan();
      return { started: true };

    case "getSettings":
      return getSettings();

    case "updateSettings": {
      const updated = await updateSettings(message.settings);
      // Restart alarm if interval changed
      if (message.settings.scanIntervalMinutes || message.settings.scanEnabled !== undefined) {
        if (updated.scanEnabled) {
          await setupAlarm(updated.scanIntervalMinutes);
        } else {
          await messenger.alarms.clear(ALARM_NAME);
        }
      }
      return updated;
    }

    case "getRules":
      return getRules();

    case "getActivityLog":
      return getActivityLog(message.limit || 50);

    case "testLlmConnection":
      return testLlmConnection(message.settings);

    case "generateRule":
      return generateRuleFromExamples(message.examples, await getSettings());

    case "getAccounts":
      return getAccountsAndFolders();

    case "runDiagnostic":
      return runDiagnostic();

    case "markAsReceipt":
      return handleMarkAsReceipt(message.messageId);

    case "markAsExpired":
      return handleMarkAsExpired(message.messageId);

    case "setManualExpiry":
      return handleSetManualExpiry(message.messageId, message.hours);

    case "undoManualAction":
      return handleUndoManualAction(message.logIndex);

    case "getMessageInfo":
      return getMessageInfo(message.messageId);

    default:
      console.warn("[MailReaper] Unknown message type:", message.type);
      return null;
  }
});

// ── Message Info ─────────────────────────────────────────────────────────────

async function getMessageInfo(messageId) {
  try {
    const msg = await messenger.messages.get(messageId);
    const headerMessageId = msg.headerMessageId;
    const info = { status: [] };

    if (!headerMessageId) {
      return info;
    }

    // Check activity log for past actions on this message.
    // Match by headerMessageId (reliable across moves) or subject+sender as fallback.
    const activityLog = await getActivityLog(500);
    const activityEntry = activityLog.find((e) => {
      if (e.undone) return false;
      if (e.headerMessageId && e.headerMessageId === headerMessageId) return true;
      if (e.subject === msg.subject && e.sender === msg.author) return true;
      return false;
    });
    if (activityEntry) {
      const icons = {
        moved: "📦", classified: "📂", deleted: "🗑️",
        tagged: "🏷️", manual_expiry_set: "⏰",
      };
      const icon = icons[activityEntry.type] || "•";
      const rule = activityEntry.rule || "";
      const reason = activityEntry.reason || "";
      const parts = [rule, reason].filter(Boolean);
      info.status.push({ icon, text: parts.join(" — ") || activityEntry.type });
      if (activityEntry.confidence && activityEntry.confidence < 1) {
        info.status.push({ icon: "🎯", text: `Confidence: ${Math.round(activityEntry.confidence * 100)}%` });
      }
      info.hasActivity = true;
    }

    // Check manual overrides
    const overrides = await getManualOverrides();
    if (overrides[headerMessageId]) {
      const override = overrides[headerMessageId];
      const expiresAt = new Date(override.expiresAt);
      const now = new Date();
      if (now > expiresAt) {
        info.status.push({ icon: "⏰", text: "Expiry elapsed — pending next scan" });
      } else {
        info.status.push({ icon: "⏰", text: `Expires ${formatRelativeTimeBackground(expiresAt)}` });
      }
      info.hasManualExpiry = true;
    }

    // Check LLM cache
    const cached = await getCachedVerdict(headerMessageId);
    if (cached) {
      if (cached.error) {
        info.status.push({ icon: "⚠️", text: `LLM error: ${cached.error}` });
      } else if (cached.expired) {
        info.status.push({ icon: "🔴", text: `LLM: expired — ${cached.reason}` });
      } else if (cached.classified) {
        info.status.push({ icon: "📂", text: `LLM: classified — ${cached.reason}` });
      } else if (cached.expiresAt) {
        const expiresAt = new Date(cached.expiresAt);
        const now = new Date();
        if (now > expiresAt) {
          info.status.push({ icon: "🔴", text: `LLM: expired — ${cached.reason}` });
        } else {
          info.status.push({ icon: "🟡", text: `LLM: expires ${formatRelativeTimeBackground(expiresAt)} — ${cached.reason}` });
        }
      } else {
        info.status.push({ icon: "🟢", text: `LLM: not time-sensitive — ${cached.reason}` });
      }
      info.hasCachedVerdict = true;
    }

    // Check evaluatedNoMatch
    const noMatchEntry = evaluatedNoMatch.get(messageId);
    if (noMatchEntry) {
      const trace = noMatchEntry.trace || [];
      if (trace.length > 0) {
        info.status.push({ icon: "⚪", text: `Checked: ${trace.join(", ")}` });
      } else {
        info.status.push({ icon: "⚪", text: "Scanned — no rule criteria matched" });
      }
      info.scannedNoMatch = true;
    }

    // If we found nothing at all, say so
    if (info.status.length === 0) {
      if (!lastScanTime) {
        info.status.push({ icon: "⚪", text: "Not scanned yet" });
      } else {
        // A scan has run but this message wasn't in it — likely not in a scanned folder,
        // or too new, or beyond maxMessagesPerScan
        const settings = await getSettings();
        const msgDate = new Date(msg.date);
        const minAgeMs = settings.minMessageAgeMinutes * 60 * 1000;
        const tooNew = (Date.now() - msgDate.getTime()) < minAgeMs;

        if (tooNew) {
          info.status.push({ icon: "⚪", text: `Not scanned — less than ${settings.minMessageAgeMinutes}m old` });
        } else {
          info.status.push({ icon: "⚪", text: "Not scanned — not in a scanned folder?" });
        }
      }
    }

    return info;
  } catch (e) {
    console.error("[MailReaper] getMessageInfo failed:", e);
    return { status: [{ icon: "⚠️", text: `Error loading info: ${e.message}` }] };
  }
}

function formatRelativeTimeBackground(date) {
  const now = Date.now();
  const diffMs = date.getTime() - now;
  const future = diffMs > 0;
  const absDiffMin = Math.round(Math.abs(diffMs) / 60000);

  if (absDiffMin < 1) return future ? "now" : "just now";
  if (absDiffMin < 60) return future ? `in ${absDiffMin}m` : `${absDiffMin}m ago`;
  const diffHr = Math.round(absDiffMin / 60);
  if (diffHr < 24) return future ? `in ${diffHr}h` : `${diffHr}h ago`;
  const diffDays = Math.round(diffHr / 24);
  return future ? `in ${diffDays}d` : `${diffDays}d ago`;
}

// ── Manual Action Handlers ───────────────────────────────────────────────────

async function handleMarkAsReceipt(messageId) {
  try {
    const msg = await messenger.messages.get(messageId);
    const originalFolderId = msg.folder?.id || null;

    // Get body snippet
    let bodySnippet = "";
    try {
      const parts = await messenger.messages.listInlineTextParts(messageId);
      bodySnippet = parts.map((p) => p.content).join("\n").substring(0, 500);
    } catch (e) {
      console.warn(`[MailReaper] Failed to get body text for message ${messageId}:`, e);
    }

    // Store training example
    await addTrainingExample({
      sender: msg.author,
      subject: msg.subject,
      sentDate: new Date(msg.date).toISOString(),
      bodySnippet,
      category: "receipt",
    });

    // Move to Paper-Trail via executeAction with a synthetic verdict
    const syntheticVerdict = {
      classified: true,
      rule: { name: "Manual: receipt", action: "move", classifyFolder: "Paper-Trail" },
      reason: "Manually marked as receipt",
      confidence: 1.0,
    };
    await executeAction(msg, syntheticVerdict);

    // Patch the activity log entry with undo info
    await patchLastActivity(messageId, {
      undoable: true,
      undoType: "classified",
      originalFolderId,
    });

    // Check if we should auto-suggest a rule (non-blocking)
    suggestRuleIfReady().catch((e) =>
      console.error("[MailReaper] Rule suggestion failed:", e)
    );

    return { success: true };
  } catch (e) {
    console.error("[MailReaper] markAsReceipt failed:", e);
    return { success: false, error: e.message };
  }
}

async function handleMarkAsExpired(messageId) {
  try {
    const msg = await messenger.messages.get(messageId);
    const originalFolderId = msg.folder?.id || null;

    // Get body snippet for the training example
    let bodySnippet = "";
    try {
      const parts = await messenger.messages.listInlineTextParts(messageId);
      bodySnippet = parts.map((p) => p.content).join("\n").substring(0, 500);
    } catch (e) {
      console.warn(`[MailReaper] Failed to get body text for message ${messageId}:`, e);
    }

    // Store as an expiry training example so the LLM learns from it
    await addTrainingExample({
      sender: msg.author,
      subject: msg.subject,
      sentDate: new Date(msg.date).toISOString(),
      bodySnippet,
      category: "expiry",
      // The user is saying "this is expired right now" — record that
      expiresAt: new Date().toISOString(),
    });

    // Invalidate any cached LLM verdict for this message
    if (msg.headerMessageId) {
      await removeCachedVerdict(msg.headerMessageId);
    }

    // Move to Expired folder
    const verdict = {
      expired: true,
      rule: { name: "Manual: expired", action: "move", id: "manual-expired" },
      expiresAt: new Date().toISOString(),
      reason: "Manually marked as expired",
      confidence: 1.0,
    };
    await executeAction(msg, verdict);

    // Patch the activity log entry with undo info
    await patchLastActivity(messageId, {
      undoable: true,
      undoType: "expired",
      originalFolderId,
    });

    return { success: true };
  } catch (e) {
    console.error("[MailReaper] markAsExpired failed:", e);
    return { success: false, error: e.message };
  }
}

async function handleSetManualExpiry(messageId, hours) {
  try {
    const msg = await messenger.messages.get(messageId);
    const headerMessageId = msg.headerMessageId;

    if (!headerMessageId) {
      return { success: false, error: "Message has no header Message-ID" };
    }

    const expiresAt = new Date(Date.now() + hours * 3600000).toISOString();
    await setManualOverride(headerMessageId, {
      expiresAt,
      setAt: new Date().toISOString(),
      subject: msg.subject,
    });

    await logActivity({
      type: "manual_expiry_set",
      messageId: msg.id,
      subject: msg.subject,
      sender: msg.author,
      rule: "Manual expiry",
      expiresAt,
      reason: `User set ${hours}h expiry`,
      undoable: true,
      undoType: "manual_expiry",
      headerMessageId,
    });

    // Clear evaluatedNoMatch so next scan re-evaluates this message
    evaluatedNoMatch.delete(msg.id);

    return { success: true, expiresAt };
  } catch (e) {
    console.error("[MailReaper] setManualExpiry failed:", e);
    return { success: false, error: e.message };
  }
}

/**
 * Patch the most recent activity log entry for a given messageId with extra fields.
 * Used to attach undo metadata after executeAction logs the entry.
 */
async function patchLastActivity(messageId, patch) {
  try {
    const { [STORAGE_KEYS.ACTIVITY_LOG]: log } =
      await messenger.storage.local.get(STORAGE_KEYS.ACTIVITY_LOG);
    if (!log) return;

    const entry = log.find((e) => e.messageId === messageId);
    if (entry) {
      Object.assign(entry, patch);
      await messenger.storage.local.set({ [STORAGE_KEYS.ACTIVITY_LOG]: log });
    } else {
      console.warn(`[MailReaper] patchLastActivity: no log entry found for message ${messageId}`);
    }
  } catch (e) {
    console.error("[MailReaper] patchLastActivity failed:", e);
  }
}

async function handleUndoManualAction(logIndex) {
  const { [STORAGE_KEYS.ACTIVITY_LOG]: log } =
    await messenger.storage.local.get(STORAGE_KEYS.ACTIVITY_LOG);
  if (!log || !log[logIndex]) {
    return { success: false, error: "Activity entry not found" };
  }

  const entry = log[logIndex];
  if (!entry.undoable) {
    return { success: false, error: "This action cannot be undone" };
  }

  try {
    if ((entry.undoType === "classified" || entry.undoType === "expired") && entry.originalFolderId) {
      // Look up the message's current ID — Thunderbird changes IDs after moves,
      // so entry.messageId is stale. Query by headerMessageId instead.
      let currentMessageId = entry.messageId;
      if (entry.headerMessageId) {
        const results = await messenger.messages.query({ headerMessageId: entry.headerMessageId });
        if (results.messages && results.messages.length > 0) {
          currentMessageId = results.messages[0].id;
        }
      }

      // Move message back to original folder
      await messenger.messages.move([currentMessageId], entry.originalFolderId);

      // Remove the most recent training example matching this message
      await removeTrainingExampleBySubject(entry.subject);

      console.log(`[MailReaper] Undid ${entry.undoType} action for "${entry.subject}"`);
    } else if (entry.undoType === "manual_expiry" && entry.headerMessageId) {
      // Remove the manual override
      await removeManualOverride(entry.headerMessageId);

      console.log(`[MailReaper] Undid manual expiry for "${entry.subject}"`);
    } else {
      return { success: false, error: "Unknown undo type" };
    }

    // Mark entry as undone (not undoable anymore)
    entry.undoable = false;
    entry.undone = true;
    await messenger.storage.local.set({ [STORAGE_KEYS.ACTIVITY_LOG]: log });

    return { success: true };
  } catch (e) {
    console.error("[MailReaper] Undo failed:", e);
    return { success: false, error: e.message };
  }
}

async function suggestRuleIfReady() {
  const settings = await getSettings();
  if (settings.llmProvider === "none") return;

  const examples = await getTrainingExamples("receipt");
  if (examples.length < 5) return;

  try {
    const result = await generateRuleFromExamples(examples, settings);
    if (!result || !result.name) return;

    const rule = {
      name: result.name,
      enabled: false,
      priority: 50,
      match: {
        senderPatterns: result.senderPatterns || [],
        subjectPatterns: result.subjectPatterns || [],
      },
      expiration: {
        type: result.expirationType === "ttl" ? "ttl" : "classify",
        ...(result.ttlHours && { hours: result.ttlHours }),
        ...(result.expirationType !== "ttl" && { category: "receipt" }),
      },
      action: result.expirationType === "ttl" ? "move" : "move",
      classifyFolder: "Paper-Trail",
      suggested: true,
    };

    await saveRule(rule);

    await messenger.notifications.create(`mailreaper-rule-${Date.now()}`, {
      type: "basic",
      title: "MailReaper",
      message: `Suggested a new receipt rule: "${result.name}". Review it in Settings.`,
    });

    console.log(`[MailReaper] Auto-suggested rule: ${result.name}`);
  } catch (e) {
    console.error("[MailReaper] Rule suggestion failed:", e);
  }
}

/**
 * Get all accounts and their folder trees for the settings UI.
 */
async function getAccountsAndFolders() {
  const accounts = await messenger.accounts.list(true);
  const result = [];

  for (const account of accounts) {
    const topFolders = account.rootFolder?.subFolders || [];
    result.push({
      id: account.id,
      name: account.name,
      type: account.type,
      folders: flattenFolders(topFolders, ""),
    });
  }

  return result;
}

/**
 * Flatten a folder tree into a list with indented paths.
 */
function flattenFolders(folders, prefix) {
  const result = [];
  for (const folder of folders) {
    result.push({
      id: folder.id,
      name: folder.name,
      path: prefix ? `${prefix}/${folder.name}` : folder.name,
      type: folder.type,
    });
    if (folder.subFolders && folder.subFolders.length > 0) {
      result.push(
        ...flattenFolders(folder.subFolders, prefix ? `${prefix}/${folder.name}` : folder.name)
      );
    }
  }
  return result;
}

// ── Diagnostic ──────────────────────────────────────────────────────────────

async function runDiagnostic() {
  const report = { steps: [] };

  try {
    // Step 1: Check settings
    const settings = await getSettings();
    report.steps.push({
      step: "Settings",
      scanEnabled: settings.scanEnabled,
      scannedFolderIds: settings.scannedFolderIds,
      maxMessagesPerScan: settings.maxMessagesPerScan,
      minMessageAgeMinutes: settings.minMessageAgeMinutes,
    });

    // Step 2: Resolve folders
    const folders = await resolveScanFolders(settings);
    report.steps.push({
      step: "Folders resolved",
      count: folders.length,
      folders: folders.map((f) => ({ id: f.id, name: f.name, type: f.type, path: f.path })),
    });

    if (folders.length === 0) {
      report.steps.push({ step: "PROBLEM", detail: "No folders resolved — nothing to scan" });
      return report;
    }

    // Step 3: Try querying the first folder
    const testFolder = folders[0];
    const cutoffDate = new Date(Date.now() - settings.minMessageAgeMinutes * 60 * 1000);

    let queryResult;
    try {
      queryResult = await messenger.messages.query({
        folderId: testFolder.id,
        toDate: cutoffDate,
      });
      report.steps.push({
        step: "Query test folder",
        folder: testFolder.name,
        messagesReturned: (queryResult.messages || []).length,
        hasMorePages: !!queryResult.id,
      });
    } catch (e) {
      report.steps.push({
        step: "Query FAILED",
        folder: testFolder.name,
        error: e.message,
      });

      // Try messages.list as fallback test
      try {
        const listResult = await messenger.messages.list(testFolder.id);
        report.steps.push({
          step: "list() fallback test",
          folder: testFolder.name,
          messagesReturned: (listResult.messages || []).length,
          hasMorePages: !!listResult.id,
        });
      } catch (e2) {
        report.steps.push({
          step: "list() also FAILED",
          error: e2.message,
        });
      }
      return report;
    }

    // Step 4: Sample a few messages and show their metadata
    const sampleMessages = (queryResult.messages || []).slice(0, 5);
    const rules = await getRules();
    const enabledRules = rules.filter((r) => r.enabled);

    report.steps.push({
      step: "Enabled rules",
      rules: enabledRules.map((r) => ({ id: r.id, name: r.name, priority: r.priority, type: r.expiration.type })),
    });

    for (const msg of sampleMessages) {
      const sample = {
        step: "Sample message",
        id: msg.id,
        subject: (msg.subject || "").substring(0, 60),
        author: msg.author,
        date: msg.date,
        age: Math.round((Date.now() - new Date(msg.date).getTime()) / 3600000) + "h",
      };
      report.steps.push(sample);
    }

  } catch (e) {
    report.steps.push({ step: "Diagnostic error", error: e.message, stack: e.stack });
  }

  return report;
}

// ── Smart cache invalidation when rules change ──────────────────────────────

/**
 * Build a fingerprint of the enabled rules' static match criteria.
 * LLM-only fields (prompts, LLM settings) are excluded so that editing
 * an LLM rule or changing AI config doesn't force a full rescan.
 */
function computeRulesFingerprint(rules) {
  const parts = rules
    .filter((r) => r.enabled)
    .sort((a, b) => a.priority - b.priority)
    .map((r) => {
      const m = r.match || {};
      return [
        r.id,
        r.enabled,
        r.expiration.type,
        r.expiration.hours || "",
        r.expiration.pattern || "",
        (m.senderPatterns || []).join("|"),
        (m.subjectPatterns || []).join("|"),
        (m.folders || []).join("|"),
        JSON.stringify(m.headerMatch || {}),
      ].join(":");
    });
  return parts.join("\n");
}

messenger.storage.onChanged.addListener((changes, area) => {
  if (area !== "local") return;

  if (changes.mailreaper_rules) {
    const oldFingerprint = currentRulesFingerprint;
    currentRulesFingerprint = null;

    if (oldFingerprint !== null) {
      evaluatedNoMatch.clear();
      console.log("[MailReaper] Rules changed, cleared evaluation cache");
    }
  }

  // Clear evaluation cache when LLM provider changes — messages that were
  // skipped because LLM was "none" need to be re-evaluated
  if (changes.mailreaper_settings) {
    const oldSettings = changes.mailreaper_settings.oldValue || {};
    const newSettings = changes.mailreaper_settings.newValue || {};
    if (oldSettings.llmProvider !== newSettings.llmProvider) {
      evaluatedNoMatch.clear();
      console.log(`[MailReaper] LLM provider changed (${oldSettings.llmProvider} → ${newSettings.llmProvider}), cleared evaluation cache`);
    }
  }
});

// ── Bootstrap ───────────────────────────────────────────────────────────────

init().catch(async (e) => {
  console.error("[MailReaper] Init failed:", e);
  lastScanError = `Initialization failed: ${e.message}`;
  await persistScanState();
});
