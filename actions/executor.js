/**
 * Action Executor — carries out expiration verdicts.
 * Handles moving to expired folder, tagging, deleting, and grace period cleanup.
 */

import { getSettings, logActivity } from "../rules/storage.js";

const EXPIRED_FOLDER_NAME = "Expired";
const MAILREAPER_TAG_KEY = "mailreaper_expired";

// Cache for folder IDs we've already resolved (keyed by "accountId:folderName")
let folderCache = new Map();

/**
 * Execute the action specified by a rule's verdict on a message.
 *
 * @param {object} message - Thunderbird MessageHeader
 * @param {object} verdict - { expired, rule, expiresAt, reason, confidence }
 */
export async function executeAction(message, verdict) {
  const { rule } = verdict;
  const settings = await getSettings();

  try {
    switch (rule.action) {
      case "move":
        await moveToFolder(message, verdict, settings);
        break;
      case "delete":
        await deleteMessage(message, verdict);
        break;
      case "tag":
        await tagAsExpired(message, verdict);
        break;
      default:
        console.warn(`[MailReaper] Unknown action: ${rule.action}`);
    }
  } catch (e) {
    console.error(`[MailReaper] Action failed for message ${message.id}:`, e);
    await logActivity({
      type: "error",
      messageId: message.id,
      subject: message.subject,
      sender: message.author,
      rule: rule.name,
      action: rule.action,
      error: e.message,
    });
    throw e;
  }
}

/**
 * Move a message to the appropriate folder (Expired or classification target).
 */
async function moveToFolder(message, verdict, settings) {
  const { rule } = verdict;

  let folderId;
  if (rule.destination) {
    folderId = rule.destination;
  } else if (verdict.classified) {
    const folderName = rule.classifyFolder || "Paper-Trail";
    folderId = await getOrCreateNamedFolder(message.folder.accountId, folderName);
  } else {
    const destinationId = settings.expiredFolderId;
    folderId = destinationId || await getOrCreateNamedFolder(message.folder.accountId, EXPIRED_FOLDER_NAME);
  }

  if (!folderId) {
    throw new Error("Could not determine destination folder");
  }

  await messenger.messages.move([message.id], folderId);

  // Store the move timestamp for grace period calculation
  if (!verdict.classified && message.headerMessageId) {
    const storageKey = `mailreaper_movedAt_${message.headerMessageId}`;
    await messenger.storage.local.set({ [storageKey]: Date.now() });
  }

  const activityType = verdict.classified ? "classified" : "moved";
  await logActivity({
    type: activityType,
    messageId: message.id,
    headerMessageId: message.headerMessageId,
    subject: message.subject,
    sender: message.author,
    rule: rule.name,
    expiresAt: verdict.expiresAt,
    reason: verdict.reason,
    confidence: verdict.confidence,
    destination: folderId,
  });

  const folderLabel = verdict.classified ? (rule.classifyFolder || "Paper-Trail") : "Expired";
  console.log(
    `[MailReaper] Moved "${message.subject}" to ${folderLabel} (rule: ${rule.name})`
  );
}

/**
 * Delete a message (moves to trash by default).
 */
async function deleteMessage(message, verdict) {
  await messenger.messages.delete([message.id], false); // false = move to trash, not permanent

  await logActivity({
    type: "deleted",
    messageId: message.id,
    headerMessageId: message.headerMessageId,
    subject: message.subject,
    sender: message.author,
    rule: verdict.rule.name,
    expiresAt: verdict.expiresAt,
    reason: verdict.reason,
    confidence: verdict.confidence,
  });

  console.log(
    `[MailReaper] Deleted "${message.subject}" (rule: ${verdict.rule.name})`
  );
}

/**
 * Tag a message as expired without moving it.
 */
async function tagAsExpired(message, verdict) {
  // Ensure our custom tag exists
  await ensureExpiredTag();

  const tagKey = verdict.rule.tag || MAILREAPER_TAG_KEY;
  const currentTags = message.tags || [];

  if (!currentTags.includes(tagKey)) {
    await messenger.messages.update(message.id, {
      tags: [...currentTags, tagKey],
    });
  }

  await logActivity({
    type: "tagged",
    messageId: message.id,
    headerMessageId: message.headerMessageId,
    subject: message.subject,
    sender: message.author,
    rule: verdict.rule.name,
    expiresAt: verdict.expiresAt,
    reason: verdict.reason,
    confidence: verdict.confidence,
    tag: tagKey,
  });

  console.log(
    `[MailReaper] Tagged "${message.subject}" as expired (rule: ${verdict.rule.name})`
  );
}

// ── Grace Period Cleanup ────────────────────────────────────────────────────

/**
 * Scan the Expired folder(s) and permanently delete messages
 * that have been there longer than the grace period.
 */
export async function cleanupGracePeriod() {
  try {
    const settings = await getSettings();
    if (!settings.gracePeriodCleanupEnabled) return;

    const accounts = await messenger.accounts.list();
    let cleanedCount = 0;

    for (const account of accounts) {
      const expiredFolder = await findNamedFolder(account.id, EXPIRED_FOLDER_NAME);
      if (!expiredFolder) continue;

      // Get all messages in the Expired folder
      const messageList = await messenger.messages.list(expiredFolder.id);
      let messages = messageList.messages;

      // Also get continuation pages
      let page = messageList;
      while (page.id) {
        page = await messenger.messages.continueList(page.id);
        messages = messages.concat(page.messages);
      }

      const now = Date.now();
      const gracePeriodMs = settings.defaultGracePeriodDays * 24 * 60 * 60 * 1000;

      for (const msg of messages) {
        // Look up when this message was moved to Expired.
        // Falls back to the message send date for messages moved before this tracking was added.
        let movedAt;
        if (msg.headerMessageId) {
          const storageKey = `mailreaper_movedAt_${msg.headerMessageId}`;
          const stored = await messenger.storage.local.get(storageKey);
          movedAt = stored[storageKey];
        }
        const referenceDate = movedAt || new Date(msg.date).getTime();

        // If the grace period has elapsed since the message was moved, delete permanently
        if (now - referenceDate > gracePeriodMs) {
          try {
            await messenger.messages.delete([msg.id], true); // true = skip trash, permanent delete
            cleanedCount++;

            // Clean up the stored move timestamp
            if (msg.headerMessageId) {
              const storageKey = `mailreaper_movedAt_${msg.headerMessageId}`;
              await messenger.storage.local.remove(storageKey);
            }

            await logActivity({
              type: "grace_deleted",
              messageId: msg.id,
              subject: msg.subject,
              sender: msg.author,
              rule: "Grace period cleanup",
              reason: `Exceeded ${settings.defaultGracePeriodDays}-day grace period`,
            });
          } catch (e) {
            console.error(`[MailReaper] Grace cleanup failed for ${msg.id}:`, e);
          }
        }
      }
    }

    if (cleanedCount > 0) {
      console.log(`[MailReaper] Grace period cleanup: permanently deleted ${cleanedCount} messages`);
    }
  } catch (e) {
    console.error("[MailReaper] Grace period cleanup failed:", e);
  }
}

// ── Folder Helpers ──────────────────────────────────────────────────────────

/**
 * Find or create a named folder for an account.
 * Tries root level first, then under a "Folders" parent (Proton Mail).
 */
async function getOrCreateNamedFolder(accountId, folderName) {
  const cacheKey = `${accountId}:${folderName}`;

  if (folderCache.has(cacheKey)) {
    return folderCache.get(cacheKey);
  }

  let folder = await findNamedFolder(accountId, folderName);

  if (!folder) {
    const account = await messenger.accounts.get(accountId, true);
    if (!account) return null;

    const rootFolders = account.rootFolder?.subFolders || [];

    // Some providers (Proton Mail) organize user folders under a "Folders" parent
    const foldersParent = rootFolders.find((f) =>
      f.name === "Folders" || f.name === "Labels"
    );

    const parentId = foldersParent ? foldersParent.id : account.rootFolder?.id;

    if (!parentId) {
      console.error(`[MailReaper] Cannot determine parent folder for "${folderName}"`);
      return null;
    }

    try {
      folder = await messenger.folders.create(parentId, folderName);
      console.log(`[MailReaper] Created "${folderName}" folder under ${foldersParent ? foldersParent.name : "root"}`);
    } catch (e) {
      console.error(`[MailReaper] Failed to create "${folderName}" folder:`, e);

      if (!foldersParent && account.rootFolder?.id) {
        try {
          folder = await messenger.folders.create(account.rootFolder.id, folderName);
          console.log(`[MailReaper] Created "${folderName}" folder (fallback)`);
        } catch (e2) {
          console.error(`[MailReaper] Fallback folder creation also failed:`, e2);
          return null;
        }
      } else {
        return null;
      }
    }
  }

  if (folder) {
    folderCache.set(cacheKey, folder.id);
    return folder.id;
  }

  return null;
}

/**
 * Find a named folder anywhere in an account's folder tree.
 */
async function findNamedFolder(accountId, folderName) {
  try {
    const account = await messenger.accounts.get(accountId, true);
    if (!account || !account.rootFolder) return null;

    return findFolderByName(account.rootFolder.subFolders || [], folderName);
  } catch (e) {
    console.error(`[MailReaper] Error finding "${folderName}" folder:`, e);
    return null;
  }
}

/**
 * Recursively search folder tree by name.
 */
function findFolderByName(folders, name) {
  for (const folder of folders) {
    if (folder.name === name) return folder;
    if (folder.subFolders && folder.subFolders.length > 0) {
      const found = findFolderByName(folder.subFolders, name);
      if (found) return found;
    }
  }
  return null;
}

/**
 * Ensure the custom "mailreaper_expired" tag exists in Thunderbird.
 */
let tagEnsured = false;
async function ensureExpiredTag() {
  if (tagEnsured) return;

  try {
    const tags = await messenger.messages.tags.list();
    const exists = tags.some((t) => t.key === MAILREAPER_TAG_KEY);

    if (!exists) {
      await messenger.messages.tags.create(
        MAILREAPER_TAG_KEY,
        "Expired (MailReaper)",
        "#999999"
      );
    }

    tagEnsured = true;
  } catch (e) {
    console.error("[MailReaper] Failed to create tag:", e);
  }
}

export { EXPIRED_FOLDER_NAME, getOrCreateNamedFolder };
