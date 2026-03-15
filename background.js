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
  const startTime = Date.now();
  let processed = 0;
  let expired = 0;
  let errors = 0;

  try {
    const settings = await getSettings();

    if (!settings.scanEnabled) {
      console.log("[MailReaper] Scanning is disabled");
      return;
    }

    const folders = await resolveScanFolders(settings);

    if (folders.length === 0) {
      console.log("[MailReaper] No folders configured for scanning");
      return;
    }

    console.log(`[MailReaper] Starting scan of ${folders.length} folder(s)...`);

    const minAgeMs = settings.minMessageAgeMinutes * 60 * 1000;
    const cutoffDate = new Date(Date.now() - minAgeMs);

    for (const folder of folders) {
      if (processed >= settings.maxMessagesPerScan) break;

      try {
        const candidates = await getCandidateMessages(folder, cutoffDate, settings);

        for (const message of candidates) {
          if (processed >= settings.maxMessagesPerScan) break;
          processed++;

          try {
            // Get full message for header access
            const fullMessage = await messenger.messages.getFull(message.id);

            // Get body text for content analysis
            let bodyText = "";
            try {
              const parts = await messenger.messages.listInlineTextParts(message.id);
              bodyText = parts.map((p) => p.content).join("\n");
            } catch {
              // Some messages may not have accessible inline parts
            }

            // Evaluate against rules
            const verdict = await evaluateMessage(message, fullMessage, bodyText);

            if (verdict && verdict.expired) {
              await executeAction(message, verdict);
              expired++;
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

    lastScanTime = new Date().toISOString();
    lastScanResults = { processed, expired, errors };

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
    console.error("[MailReaper] Scan failed:", e);
  } finally {
    scanInProgress = false;
  }
}

/**
 * Resolve which folders to scan based on settings.
 */
async function resolveScanFolders(settings) {
  if (settings.scannedFolderIds && settings.scannedFolderIds.length > 0) {
    // User has configured specific folders
    const folders = [];
    for (const folderId of settings.scannedFolderIds) {
      try {
        const folder = await messenger.folders.get(folderId);
        if (folder) folders.push(folder);
      } catch (e) {
        console.warn(`[MailReaper] Could not resolve folder ${folderId}:`, e);
      }
    }
    return folders;
  }

  // Default: scan all inbox folders across accounts
  const accounts = await messenger.accounts.list();
  const folders = [];

  for (const account of accounts) {
    const fullAccount = await messenger.accounts.get(account.id, true);
    if (fullAccount && fullAccount.folders) {
      for (const folder of fullAccount.folders) {
        if (folder.type === "inbox") {
          folders.push(folder);
        }
      }
    }
  }

  return folders;
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

    default:
      console.warn("[MailReaper] Unknown message type:", message.type);
      return null;
  }
});

/**
 * Get all accounts and their folder trees for the settings UI.
 */
async function getAccountsAndFolders() {
  const accounts = await messenger.accounts.list();
  const result = [];

  for (const account of accounts) {
    const full = await messenger.accounts.get(account.id, true);
    result.push({
      id: account.id,
      name: account.name,
      type: account.type,
      folders: flattenFolders(full.folders || [], ""),
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

// ── Bootstrap ───────────────────────────────────────────────────────────────

init().catch((e) => console.error("[MailReaper] Init failed:", e));
