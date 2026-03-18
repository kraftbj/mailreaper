/**
 * Storage layer for MailReaper rules and settings.
 * Uses browser.storage.local for persistence across sessions.
 */

import { DEFAULT_RULES } from "./defaults.js";

// Simple per-key write queue to prevent read-modify-write race conditions.
const _locks = new Map();
async function withLock(key, fn) {
  const prev = _locks.get(key) || Promise.resolve();
  const next = prev.then(fn, fn);
  _locks.set(key, next);
  // Clean up after completion so the map doesn't grow
  next.then(() => { if (_locks.get(key) === next) _locks.delete(key); });
  return next;
}

const STORAGE_KEYS = {
  RULES: "mailreaper_rules",
  SETTINGS: "mailreaper_settings",
  ACTIVITY_LOG: "mailreaper_activity",
  LLM_CACHE: "mailreaper_llm_cache",
  INITIALIZED: "mailreaper_initialized",
  MANUAL_OVERRIDES: "mailreaper_manual_overrides",
  TRAINING_EXAMPLES: "mailreaper_training_examples",
};

const DEFAULT_SETTINGS = {
  // Scan behavior
  scanIntervalMinutes: 30,
  minMessageAgeMinutes: 60, // Don't analyze anything less than 1 hour old
  maxMessagesPerScan: 100, // Rate limit per scan cycle
  scanEnabled: true,

  // Folders
  scannedFolderIds: [], // Empty = scan all inbox folders
  expiredFolderId: null, // Created on first run

  // LLM configuration
  llmProvider: "none", // "none" | "gemini" | "ollama"
  geminiApiKey: "",
  geminiModel: "gemini-2.5-flash",
  ollamaEndpoint: "http://localhost:11434",
  ollamaModel: "qwen2.5:7b",
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
 * Initialize storage with defaults on first run, and merge new default
 * patterns into existing built-in rules on every startup.
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
  } else {
    await mergeBuiltinRules();
  }
}

/**
 * Merge new patterns from DEFAULT_RULES into stored built-in rules.
 * Adds any patterns from defaults that aren't already present, without
 * removing user-added patterns. Also adds any new built-in rules that
 * didn't exist before.
 */
async function mergeBuiltinRules() {
  const rules = await getRules();
  let changed = false;

  for (const defaultRule of DEFAULT_RULES) {
    const stored = rules.find((r) => r.id === defaultRule.id);

    if (!stored) {
      // New built-in rule — add it
      rules.push(defaultRule);
      changed = true;
      console.log(`[MailReaper] Added new built-in rule: ${defaultRule.name}`);
      continue;
    }

    if (!stored.builtin) continue;

    // Merge match patterns (add new ones, keep existing)
    for (const key of ["senderPatterns", "subjectPatterns"]) {
      const defaultPatterns = defaultRule.match[key] || [];
      const storedPatterns = stored.match[key] || [];
      const storedSet = new Set(storedPatterns.map((p) => p.toLowerCase()));
      const newPatterns = defaultPatterns.filter((p) => !storedSet.has(p.toLowerCase()));

      if (newPatterns.length > 0) {
        stored.match[key] = [...storedPatterns, ...newPatterns];
        changed = true;
        console.log(`[MailReaper] Merged ${newPatterns.length} new ${key} into rule "${stored.name}"`);
      }
    }
  }

  if (changed) {
    await messenger.storage.local.set({ [STORAGE_KEYS.RULES]: rules });
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
  return withLock(STORAGE_KEYS.ACTIVITY_LOG, async () => {
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
  });
}

export async function clearActivityLog() {
  await messenger.storage.local.set({ [STORAGE_KEYS.ACTIVITY_LOG]: [] });
}

// ── LLM Cache ───────────────────────────────────────────────────────────────

// Positive verdicts (expired or classified) are re-checked after 24h.
// "Not time-sensitive" verdicts are cached for 7 days.
const CACHE_TTL_EXPIRED_MS = 24 * 60 * 60 * 1000;
const CACHE_TTL_NOT_SENSITIVE_MS = 7 * 24 * 60 * 60 * 1000;
const CACHE_TTL_ERROR_MS = 10 * 60 * 1000; // Retry errors after 10 minutes

export async function getCachedVerdict(messageIdHeader) {
  const { [STORAGE_KEYS.LLM_CACHE]: cache } =
    await messenger.storage.local.get(STORAGE_KEYS.LLM_CACHE);
  if (!cache) return null;

  const entry = cache[messageIdHeader];
  if (!entry) return null;

  const age = Date.now() - entry.cachedAt;
  let ttl;
  if (entry.verdict?.error) {
    ttl = CACHE_TTL_ERROR_MS;
  } else if (entry.verdict?.expired || entry.verdict?.classified) {
    ttl = CACHE_TTL_EXPIRED_MS;
  } else {
    ttl = CACHE_TTL_NOT_SENSITIVE_MS;
  }

  if (age > ttl) {
    return null;
  }

  return entry.verdict;
}

export async function setCachedVerdict(messageIdHeader, verdict) {
  return withLock(STORAGE_KEYS.LLM_CACHE, async () => {
    const { [STORAGE_KEYS.LLM_CACHE]: cache } =
      await messenger.storage.local.get(STORAGE_KEYS.LLM_CACHE);
    const updated = cache || {};

    updated[messageIdHeader] = {
      verdict,
      cachedAt: Date.now(),
    };

    // Prune oldest entries when cache exceeds 5000 (by insertion time)
    const entries = Object.entries(updated);
    if (entries.length > 5000) {
      entries.sort((a, b) => b[1].cachedAt - a[1].cachedAt);
      const pruned = Object.fromEntries(entries.slice(0, 5000));
      await messenger.storage.local.set({ [STORAGE_KEYS.LLM_CACHE]: pruned });
    } else {
      await messenger.storage.local.set({ [STORAGE_KEYS.LLM_CACHE]: updated });
    }
  });
}

export async function removeCachedVerdict(messageIdHeader) {
  const { [STORAGE_KEYS.LLM_CACHE]: cache } =
    await messenger.storage.local.get(STORAGE_KEYS.LLM_CACHE);
  if (!cache || !cache[messageIdHeader]) return;
  delete cache[messageIdHeader];
  await messenger.storage.local.set({ [STORAGE_KEYS.LLM_CACHE]: cache });
}

export async function clearLlmCache() {
  await messenger.storage.local.set({ [STORAGE_KEYS.LLM_CACHE]: {} });
}

// ── Manual Overrides ─────────────────────────────────────────────────────────

export async function getManualOverrides() {
  const { [STORAGE_KEYS.MANUAL_OVERRIDES]: overrides } =
    await messenger.storage.local.get(STORAGE_KEYS.MANUAL_OVERRIDES);
  return overrides || {};
}

export async function setManualOverride(messageIdHeader, override) {
  const overrides = await getManualOverrides();
  overrides[messageIdHeader] = override;
  await messenger.storage.local.set({ [STORAGE_KEYS.MANUAL_OVERRIDES]: overrides });
}

export async function removeManualOverride(messageIdHeader) {
  const overrides = await getManualOverrides();
  delete overrides[messageIdHeader];
  await messenger.storage.local.set({ [STORAGE_KEYS.MANUAL_OVERRIDES]: overrides });
}

// ── Training Examples ────────────────────────────────────────────────────────

const MAX_TRAINING_EXAMPLES = 100;

export async function addTrainingExample(example) {
  const { [STORAGE_KEYS.TRAINING_EXAMPLES]: all } =
    await messenger.storage.local.get(STORAGE_KEYS.TRAINING_EXAMPLES);
  const examples = all || [];

  examples.push({ ...example, addedAt: new Date().toISOString() });

  // Cap at MAX_TRAINING_EXAMPLES (keep most recent)
  while (examples.length > MAX_TRAINING_EXAMPLES) {
    examples.shift();
  }

  await messenger.storage.local.set({ [STORAGE_KEYS.TRAINING_EXAMPLES]: examples });
}

export async function getTrainingExamples(category) {
  const { [STORAGE_KEYS.TRAINING_EXAMPLES]: all } =
    await messenger.storage.local.get(STORAGE_KEYS.TRAINING_EXAMPLES);
  if (!all) return [];
  return category ? all.filter((ex) => ex.category === category) : all;
}

export async function removeTrainingExampleBySubject(subject) {
  const { [STORAGE_KEYS.TRAINING_EXAMPLES]: all } =
    await messenger.storage.local.get(STORAGE_KEYS.TRAINING_EXAMPLES);
  if (!all || all.length === 0) return;

  // Remove the most recent example matching this subject
  for (let i = all.length - 1; i >= 0; i--) {
    if (all[i].subject === subject) {
      all.splice(i, 1);
      await messenger.storage.local.set({ [STORAGE_KEYS.TRAINING_EXAMPLES]: all });
      return;
    }
  }
}

export async function clearTrainingExamples(category) {
  if (!category) {
    await messenger.storage.local.set({ [STORAGE_KEYS.TRAINING_EXAMPLES]: [] });
    return;
  }
  const { [STORAGE_KEYS.TRAINING_EXAMPLES]: all } =
    await messenger.storage.local.get(STORAGE_KEYS.TRAINING_EXAMPLES);
  const filtered = (all || []).filter((ex) => ex.category !== category);
  await messenger.storage.local.set({ [STORAGE_KEYS.TRAINING_EXAMPLES]: filtered });
}

// ── Export all storage keys for debugging ────────────────────────────────────

export { STORAGE_KEYS, DEFAULT_SETTINGS };
