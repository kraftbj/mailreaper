/**
 * Storage layer for MailReaper rules and settings.
 * Uses browser.storage.local for persistence across sessions.
 */

import { DEFAULT_RULES } from "./defaults.js";

const STORAGE_KEYS = {
  RULES: "mailreaper_rules",
  SETTINGS: "mailreaper_settings",
  ACTIVITY_LOG: "mailreaper_activity",
  LLM_CACHE: "mailreaper_llm_cache",
  INITIALIZED: "mailreaper_initialized",
};

const DEFAULT_SETTINGS = {
  // Scan behavior
  scanIntervalMinutes: 30,
  minMessageAgeMinutes: 60, // Don't analyze anything less than 1 hour old
  maxMessagesPerScan: 100, // Rate limit per scan cycle
  scanEnabled: true,

  // Folders
  scannedFolderIds: [], // Empty = user must configure
  expiredFolderId: null, // Created on first run

  // LLM configuration
  llmProvider: "none", // "none" | "gemini" | "ollama"
  geminiApiKey: "",
  geminiModel: "gemini-2.5-flash",
  ollamaEndpoint: "http://localhost:11434",
  ollamaModel: "llama3.2:3b",
  llmMaxSnippetLength: 2000,
  llmConfidenceThreshold: 0.7, // Minimum confidence to act on LLM verdict
  llmMetadataOnly: false, // If true, don't send body content to LLM

  // Actions
  defaultGracePeriodDays: 7,
  gracePeriodCleanupEnabled: true,
  notifyOnExpiration: false,

  // Activity log
  activityLogMaxEntries: 500,
};

/**
 * Initialize storage with defaults on first run.
 */
export async function initializeStorage() {
  const { [STORAGE_KEYS.INITIALIZED]: initialized } =
    await messenger.storage.local.get(STORAGE_KEYS.INITIALIZED);

  if (!initialized) {
    await messenger.storage.local.set({
      [STORAGE_KEYS.RULES]: DEFAULT_RULES,
      [STORAGE_KEYS.SETTINGS]: DEFAULT_SETTINGS,
      [STORAGE_KEYS.ACTIVITY_LOG]: [],
      [STORAGE_KEYS.LLM_CACHE]: {},
      [STORAGE_KEYS.INITIALIZED]: true,
    });
    console.log("[MailReaper] Storage initialized with defaults");
  }
}

// ── Rules CRUD ──────────────────────────────────────────────────────────────

export async function getRules() {
  const { [STORAGE_KEYS.RULES]: rules } = await messenger.storage.local.get(
    STORAGE_KEYS.RULES
  );
  return rules || DEFAULT_RULES;
}

export async function getRule(ruleId) {
  const rules = await getRules();
  return rules.find((r) => r.id === ruleId) || null;
}

export async function saveRule(rule) {
  const rules = await getRules();
  const index = rules.findIndex((r) => r.id === rule.id);

  if (index >= 0) {
    rules[index] = { ...rules[index], ...rule };
  } else {
    // New rule — assign ID if missing
    if (!rule.id) {
      rule.id = `user-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    }
    rules.push(rule);
  }

  await messenger.storage.local.set({ [STORAGE_KEYS.RULES]: rules });
  return rule;
}

export async function deleteRule(ruleId) {
  const rules = await getRules();
  const filtered = rules.filter((r) => r.id !== ruleId);
  await messenger.storage.local.set({ [STORAGE_KEYS.RULES]: filtered });
}

export async function reorderRules(orderedIds) {
  const rules = await getRules();
  const ruleMap = new Map(rules.map((r) => [r.id, r]));
  const reordered = orderedIds
    .map((id, i) => {
      const rule = ruleMap.get(id);
      if (rule) {
        rule.priority = (i + 1) * 10;
        return rule;
      }
      return null;
    })
    .filter(Boolean);

  // Append any rules not in the ordered list (shouldn't happen, but safety)
  for (const rule of rules) {
    if (!orderedIds.includes(rule.id)) {
      reordered.push(rule);
    }
  }

  await messenger.storage.local.set({ [STORAGE_KEYS.RULES]: reordered });
}

export async function resetRulesToDefaults() {
  await messenger.storage.local.set({ [STORAGE_KEYS.RULES]: DEFAULT_RULES });
}

// ── Settings ────────────────────────────────────────────────────────────────

export async function getSettings() {
  const { [STORAGE_KEYS.SETTINGS]: settings } =
    await messenger.storage.local.get(STORAGE_KEYS.SETTINGS);
  // Merge with defaults to pick up any new settings added in updates
  return { ...DEFAULT_SETTINGS, ...(settings || {}) };
}

export async function updateSettings(updates) {
  const current = await getSettings();
  const merged = { ...current, ...updates };
  await messenger.storage.local.set({ [STORAGE_KEYS.SETTINGS]: merged });
  return merged;
}

// ── Activity Log ────────────────────────────────────────────────────────────

export async function getActivityLog(limit = 50) {
  const { [STORAGE_KEYS.ACTIVITY_LOG]: log } =
    await messenger.storage.local.get(STORAGE_KEYS.ACTIVITY_LOG);
  const entries = log || [];
  return entries.slice(0, limit);
}

export async function logActivity(entry) {
  const settings = await getSettings();
  const { [STORAGE_KEYS.ACTIVITY_LOG]: log } =
    await messenger.storage.local.get(STORAGE_KEYS.ACTIVITY_LOG);
  const entries = log || [];

  entries.unshift({
    ...entry,
    timestamp: new Date().toISOString(),
  });

  // Trim to max
  while (entries.length > settings.activityLogMaxEntries) {
    entries.pop();
  }

  await messenger.storage.local.set({ [STORAGE_KEYS.ACTIVITY_LOG]: entries });
}

export async function clearActivityLog() {
  await messenger.storage.local.set({ [STORAGE_KEYS.ACTIVITY_LOG]: [] });
}

// ── LLM Cache ───────────────────────────────────────────────────────────────

const CACHE_TTL_MS = 24 * 60 * 60 * 1000; // Re-check after 24 hours

export async function getCachedVerdict(messageIdHeader) {
  const { [STORAGE_KEYS.LLM_CACHE]: cache } =
    await messenger.storage.local.get(STORAGE_KEYS.LLM_CACHE);
  if (!cache) return null;

  const entry = cache[messageIdHeader];
  if (!entry) return null;

  // Check if cache entry has expired
  if (Date.now() - entry.cachedAt > CACHE_TTL_MS) {
    return null;
  }

  return entry.verdict;
}

export async function setCachedVerdict(messageIdHeader, verdict) {
  const { [STORAGE_KEYS.LLM_CACHE]: cache } =
    await messenger.storage.local.get(STORAGE_KEYS.LLM_CACHE);
  const updated = cache || {};

  updated[messageIdHeader] = {
    verdict,
    cachedAt: Date.now(),
  };

  // Prune old entries (keep last 1000)
  const entries = Object.entries(updated);
  if (entries.length > 1000) {
    entries.sort((a, b) => b[1].cachedAt - a[1].cachedAt);
    const pruned = Object.fromEntries(entries.slice(0, 1000));
    await messenger.storage.local.set({ [STORAGE_KEYS.LLM_CACHE]: pruned });
  } else {
    await messenger.storage.local.set({ [STORAGE_KEYS.LLM_CACHE]: updated });
  }
}

export async function clearLlmCache() {
  await messenger.storage.local.set({ [STORAGE_KEYS.LLM_CACHE]: {} });
}

// ── Export all storage keys for debugging ────────────────────────────────────

export { STORAGE_KEYS, DEFAULT_SETTINGS };
