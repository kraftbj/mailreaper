/**
 * Rule Engine — evaluates messages against rules to determine expiration.
 *
 * Returns a verdict for each message: { expired: bool, rule, expiresAt, reason }
 */

import { getRules, getSettings, getCachedVerdict, setCachedVerdict } from "./storage.js";
import { analyzeMesageWithLlm } from "../llm/adapter.js";

/**
 * Evaluate a single message against all enabled rules (in priority order).
 * Returns the first matching verdict, or null if no rule matches.
 *
 * @param {object} message - Thunderbird MessageHeader object
 * @param {object} fullMessage - Result of messages.getFull() (for headers/body)
 * @param {object} bodyText - Plain text body content
 * @returns {Promise<object|null>} Verdict or null
 */
export async function evaluateMessage(message, fullMessage, bodyText) {
  const rules = await getRules();
  const settings = await getSettings();
  const enabledRules = rules
    .filter((r) => r.enabled)
    .sort((a, b) => a.priority - b.priority);

  for (const rule of enabledRules) {
    // Check if message matches this rule's criteria
    if (!matchesRule(message, rule)) {
      continue;
    }

    // Evaluate expiration based on rule type
    const verdict = await evaluateExpiration(
      message,
      fullMessage,
      bodyText,
      rule,
      settings
    );

    if (verdict) {
      return verdict;
    }
  }

  return null;
}

/**
 * Check if a message matches a rule's criteria (sender, subject, folder, headers).
 */
function matchesRule(message, rule) {
  const { match } = rule;

  // Folder filter
  if (match.folders && match.folders.length > 0) {
    if (!match.folders.includes(message.folder?.path)) {
      return false;
    }
  }

  // Sender patterns
  if (match.senderPatterns && match.senderPatterns.length > 0) {
    const sender = (message.author || "").toLowerCase();
    const senderMatch = match.senderPatterns.some((pattern) =>
      globMatch(sender, pattern.toLowerCase())
    );
    if (!senderMatch) return false;
  }

  // Subject patterns
  if (match.subjectPatterns && match.subjectPatterns.length > 0) {
    const subject = (message.subject || "").toLowerCase();
    const subjectMatch = match.subjectPatterns.some((pattern) =>
      globMatch(subject, pattern.toLowerCase())
    );
    if (!subjectMatch) return false;
  }

  // Header match (special case for Expires header)
  if (match.headerMatch && Object.keys(match.headerMatch).length > 0) {
    // Header matching requires fullMessage, handled in evaluateExpiration
    // For now, we pass — the expiration evaluator will check
  }

  return true;
}

/**
 * Evaluate whether a message has expired based on the rule's expiration type.
 */
async function evaluateExpiration(message, fullMessage, bodyText, rule, settings) {
  const now = new Date();
  const sentDate = new Date(message.date);

  switch (rule.expiration.type) {
    case "ttl": {
      const ttlMs = rule.expiration.hours * 60 * 60 * 1000;
      const expiresAt = new Date(sentDate.getTime() + ttlMs);
      if (now > expiresAt) {
        return {
          expired: true,
          rule,
          expiresAt: expiresAt.toISOString(),
          reason: `TTL expired: ${rule.expiration.hours}h after send date`,
          confidence: 1.0,
        };
      }
      return null;
    }

    case "header": {
      const expiresHeader = getHeader(fullMessage, "Expires");
      if (!expiresHeader) return null;

      try {
        const expiresAt = new Date(expiresHeader);
        if (isNaN(expiresAt.getTime())) return null;

        if (now > expiresAt) {
          return {
            expired: true,
            rule,
            expiresAt: expiresAt.toISOString(),
            reason: `Expires header: ${expiresHeader}`,
            confidence: 1.0,
          };
        }
      } catch {
        console.warn(`[MailReaper] Invalid Expires header: ${expiresHeader}`);
      }
      return null;
    }

    case "content-regex": {
      if (!bodyText || !rule.expiration.pattern) return null;

      try {
        const regex = new RegExp(rule.expiration.pattern, "i");
        const match = bodyText.match(regex);
        if (match && match[1]) {
          const expiresAt = new Date(match[1]);
          if (!isNaN(expiresAt.getTime()) && now > expiresAt) {
            return {
              expired: true,
              rule,
              expiresAt: expiresAt.toISOString(),
              reason: `Content regex matched: ${match[0]}`,
              confidence: 0.9,
            };
          }
        }
      } catch (e) {
        console.warn(`[MailReaper] Regex error in rule ${rule.id}:`, e);
      }
      return null;
    }

    case "llm": {
      if (settings.llmProvider === "none") {
        return null; // LLM not configured, skip
      }

      // Check cache first using Message-ID header
      const messageIdHeader = getHeader(fullMessage, "Message-ID") || message.headerMessageId;
      if (messageIdHeader) {
        const cached = await getCachedVerdict(messageIdHeader);
        if (cached) {
          if (cached.expired) {
            return { ...cached, rule };
          }
          return null; // Cached as "not expired"
        }
      }

      // Call LLM
      try {
        const snippet = settings.llmMetadataOnly
          ? null
          : (bodyText || "").substring(0, settings.llmMaxSnippetLength);

        const llmResult = await analyzeMesageWithLlm({
          sender: message.author,
          subject: message.subject,
          sentDate: sentDate.toISOString(),
          bodySnippet: snippet,
          customPrompt: rule.expiration.prompt || null,
        }, settings);

        // Cache the result
        if (messageIdHeader) {
          await setCachedVerdict(messageIdHeader, {
            expired: llmResult.isTimeSensitive && llmResult.expiresAt && now > new Date(llmResult.expiresAt),
            expiresAt: llmResult.expiresAt,
            reason: llmResult.reason,
            confidence: llmResult.confidence,
          });
        }

        if (
          llmResult.isTimeSensitive &&
          llmResult.expiresAt &&
          llmResult.confidence >= settings.llmConfidenceThreshold
        ) {
          const expiresAt = new Date(llmResult.expiresAt);
          if (now > expiresAt) {
            return {
              expired: true,
              rule,
              expiresAt: expiresAt.toISOString(),
              reason: `LLM: ${llmResult.reason}`,
              confidence: llmResult.confidence,
            };
          }
        }
      } catch (e) {
        console.error(`[MailReaper] LLM analysis failed for message ${message.id}:`, e);
      }
      return null;
    }

    default:
      console.warn(`[MailReaper] Unknown expiration type: ${rule.expiration.type}`);
      return null;
  }
}

/**
 * Extract a header value from a full message object.
 */
function getHeader(fullMessage, headerName) {
  if (!fullMessage || !fullMessage.headers) return null;
  const key = headerName.toLowerCase();
  const values = fullMessage.headers[key];
  if (Array.isArray(values) && values.length > 0) {
    return values[0];
  }
  return null;
}

/**
 * Simple glob matching: supports * (any chars) and ? (single char).
 * Not a full glob implementation, but sufficient for email patterns.
 */
function globMatch(str, pattern) {
  // Escape regex special chars except * and ?
  const regexStr = pattern
    .replace(/[.+^${}()|[\]\\]/g, "\\$&")
    .replace(/\*/g, ".*")
    .replace(/\?/g, ".");

  try {
    return new RegExp(`^${regexStr}$`).test(str);
  } catch {
    return false;
  }
}

export { globMatch, getHeader };
