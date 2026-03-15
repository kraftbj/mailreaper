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

import { initializeStorage, getSettings, getRules, getActivityLog, logActivity, updateSettings } from "./rules/storage.js";
import { evaluateMessage } from "./rules/engine.js";
import { executeAction, cleanupGracePeriod } from "./actions/executor.js";
import { testLlmConnection, generateRuleFromExamples } from "./llm/adapter.js";

const ALARM_NAME = "mailreaper-scan";
const GRACE_ALARM_NAME = "mailreaper-grace-cleanup";

let scanInProgress = false;
let lastScanTime = null;
let lastScanResults = { processed: 0, expired: 0, errors: 0 };
let lastScanError = null;

// Tracks messages already evaluated with no match.
// Keyed by message ID, value is the rules fingerprint at evaluation time.
// When rules change, only messages affected by the change are re-evaluated.
let evaluatedNoMatch = new Map();
let currentRulesFingerprint = null;

// ── Initialization ──────────────────────────────────────────────────────────

async function init() {
  console.log("[MailReaper] Initializing...");

  await initializeStorage();
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
    await runScan();
  } else if (alarm.name === GRACE_ALARM_NAME) {
    await cleanupGracePeriod();
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

  // Update results in real-time so the popup always reflects progress
  function updateProgress() {
    lastScanTime = new Date().toISOString();
    lastScanResults = { processed, expired, errors };
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

    const cachedCount = evaluatedNoMatch.size;
    console.log(`[MailReaper] Starting scan of ${folders.length} folder(s):`, folders.map((f) => f.path || f.name));
    console.log(`[MailReaper] Skipping ${cachedCount} previously evaluated messages`);

    const minAgeMs = settings.minMessageAgeMinutes * 60 * 1000;
    const cutoffDate = new Date(Date.now() - minAgeMs);

    for (const folder of folders) {
      if (processed >= settings.maxMessagesPerScan) break;

      try {
        const candidates = await getCandidateMessages(folder, cutoffDate, settings);

        for (const message of candidates) {
          if (processed >= settings.maxMessagesPerScan) break;
          processed++;

          // Update progress every 50 messages
          if (processed % 50 === 0) updateProgress();

          try {
            // Skip messages we've already evaluated with no match
            // under the current rules fingerprint
            if (evaluatedNoMatch.get(message.id) === fingerprint) continue;

            // Evaluate against rules — full message data is fetched lazily
            // only when a rule actually needs it (e.g. Expires header, LLM)
            const verdict = await evaluateMessage(
              message,
              () => messenger.messages.getFull(message.id),
              async () => {
                try {
                  const parts = await messenger.messages.listInlineTextParts(message.id);
                  return parts.map((p) => p.content).join("\n");
                } catch {
                  return "";
                }
              }
            );

            if (verdict && verdict.expired) {
              await executeAction(message, verdict);
              expired++;
            } else if (!verdict) {
              evaluatedNoMatch.set(message.id, fingerprint);
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
      await messenger.notifications.create(`mailreaper-scan-${Date.now()}`, {
        type: "basic",
        title: "MailReaper",
        message: `Cleaned up ${expired} expired email${expired !== 1 ? "s" : ""}.`,
      });
    }
  } catch (e) {
    lastScanError = e.message;
    console.error("[MailReaper] Scan failed:", e);
  } finally {
    scanInProgress = false;
    updateProgress();
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
    console.warn("[MailReaper] No configured folders matched, falling back to Inbox");
  }

  // Default: scan all inbox folders
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

    default:
      console.warn("[MailReaper] Unknown message type:", message.type);
      return null;
  }
});

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
  if (area === "local" && changes.mailreaper_rules) {
    const oldFingerprint = currentRulesFingerprint;
    // Fingerprint will be recomputed on next scan; clear it to force recompute
    currentRulesFingerprint = null;

    if (oldFingerprint !== null) {
      evaluatedNoMatch.clear();
      console.log("[MailReaper] Rules changed, cleared evaluation cache");
    }
  }
});

// ── Bootstrap ───────────────────────────────────────────────────────────────

init().catch((e) => console.error("[MailReaper] Init failed:", e));
