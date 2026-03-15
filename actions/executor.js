/**
 * Action Executor — carries out expiration verdicts.
 * Handles moving to expired folder, tagging, deleting, and grace period cleanup.
 */

import { getSettings, logActivity } from "../rules/storage.js";

const EXPIRED_FOLDER_NAME = "Expired";
const MAILREAPER_TAG_KEY = "mailreaper_expired";

// Cache for folder IDs we've already resolved
let expiredFolderCache = new Map(); // accountId -> folderId

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
        await moveToExpired(message, verdict, settings);
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
  }
}

/**
 * Move a message to the Expired folder for its account.
 */
async function moveToExpired(message, verdict, settings) {
  const destinationId =
    verdict.rule.destination || settings.expiredFolderId;

  let folderId;
  if (destinationId) {
    folderId = destinationId;
  } else {
    // Find or create the Expired folder for this account
    folderId = await getOrCreateExpiredFolder(message.folder.accountId);
  }

  if (!folderId) {
    throw new Error("Could not determine expired folder");
  }

  await messenger.messages.move([message.id], folderId);

  await logActivity({
    type: "moved",
    messageId: message.id,
    subject: message.subject,
    sender: message.author,
    rule: verdict.rule.name,
    expiresAt: verdict.expiresAt,
    reason: verdict.reason,
    confidence: verdict.confidence,
    destination: folderId,
  });

  console.log(
    `[MailReaper] Moved "${message.subject}" to Expired (rule: ${verdict.rule.name})`
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
  const settings = await getSettings();
  if (!settings.gracePeriodCleanupEnabled) return;

  const accounts = await messenger.accounts.list();
  let cleanedCount = 0;

  for (const account of accounts) {
    const expiredFolder = await findExpiredFolder(account.id);
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

    for (const msg of messages) {
      // Check the activity log for when this message was moved
      // Alternatively, use the message's date in the Expired folder
      // For simplicity, we use the message's original date + rule grace period + some buffer
      const messageDate = new Date(msg.date).getTime();
      const gracePeriodMs = settings.defaultGracePeriodDays * 24 * 60 * 60 * 1000;

      // If the message's send date + grace period has passed, delete permanently
      if (now - messageDate > gracePeriodMs) {
        try {
          await messenger.messages.delete([msg.id], true); // true = skip trash, permanent delete
          cleanedCount++;

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
}

// ── Folder Helpers ──────────────────────────────────────────────────────────

/**
 * Find or create the "Expired" folder for an account.
 * Tries root level first, then under a "Folders" parent (Proton Mail).
 */
async function getOrCreateExpiredFolder(accountId) {
  // Check cache
  if (expiredFolderCache.has(accountId)) {
    return expiredFolderCache.get(accountId);
  }

  // Look for existing folder anywhere in the tree
  let folder = await findExpiredFolder(accountId);

  if (!folder) {
    // Try creating at root level first
    const account = await messenger.accounts.get(accountId, true);
    if (!account) return null;

    const rootFolders = account.rootFolder?.subFolders || [];

    // Some providers (Proton Mail) organize user folders under a "Folders" parent
    const foldersParent = rootFolders.find((f) =>
      f.name === "Folders" || f.name === "Labels"
    );

    const parentId = foldersParent ? foldersParent.id : account.rootFolder?.id;

    if (!parentId) {
      console.error("[MailReaper] Cannot determine parent folder for Expired folder");
      return null;
    }

    try {
      folder = await messenger.folders.create(parentId, EXPIRED_FOLDER_NAME);
      console.log(`[MailReaper] Created "${EXPIRED_FOLDER_NAME}" folder under ${foldersParent ? foldersParent.name : "root"}`);
    } catch (e) {
      console.error(`[MailReaper] Failed to create Expired folder:`, e);

      // If root failed and there's a Folders parent we didn't try, try that
      if (!foldersParent && account.rootFolder?.id) {
        // Try creating under root folder ID directly
        try {
          folder = await messenger.folders.create(account.rootFolder.id, EXPIRED_FOLDER_NAME);
          console.log(`[MailReaper] Created "${EXPIRED_FOLDER_NAME}" folder (fallback)`);
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
    expiredFolderCache.set(accountId, folder.id);
    return folder.id;
  }

  return null;
}

/**
 * Find the Expired folder anywhere in an account's folder tree.
 */
async function findExpiredFolder(accountId) {
  try {
    const account = await messenger.accounts.get(accountId, true);
    if (!account || !account.rootFolder) return null;

    return findFolderByName(account.rootFolder.subFolders || [], EXPIRED_FOLDER_NAME);
  } catch (e) {
    console.error(`[MailReaper] Error finding expired folder:`, e);
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

export { EXPIRED_FOLDER_NAME };
