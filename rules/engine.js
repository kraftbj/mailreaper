/**
 * Rule Engine — evaluates messages against rules to determine expiration.
 *
 * Returns a verdict for each message: { expired: bool, rule, expiresAt, reason }
 */

import { getRules, getSettings, getCachedVerdict, setCachedVerdict } from "./storage.js";
import { analyzeMesageWithLlm, classifyMessageWithLlm } from "../llm/adapter.js";

/**
 * Evaluate a single message against all enabled rules (in priority order).
 * Returns the first matching verdict, or null if no rule matches.
 *
 * fullMessage and bodyText are fetched lazily — only when a rule needs them.
 *
 * @param {object} message - Thunderbird MessageHeader object
 * @param {function} getFullMessage - async function returning messages.getFull() result
 * @param {function} getBodyText - async function returning plain text body
 * @returns {Promise<object|null>} Verdict or null
 */
export async function evaluateMessage(message, getFullMessage, getBodyText) {
  const rules = await getRules();
  const settings = await getSettings();
  const enabledRules = rules
    .filter((r) => r.enabled)
    .sort((a, b) => a.priority - b.priority);

  // Cache lazy fetches so they're only called once per message
  let fullMessage = undefined;
  let bodyText = undefined;

  async function lazyFull() {
    if (fullMessage === undefined) {
      fullMessage = await getFullMessage();
    }
    return fullMessage;
  }

  async function lazyBody() {
    if (bodyText === undefined) {
      bodyText = await getBodyText();
    }
    return bodyText;
  }

  for (const rule of enabledRules) {
    // Check if message matches this rule's criteria (uses only MessageHeader data)
    if (!matchesRule(message, rule)) {
      continue;
    }

    // Evaluate expiration based on rule type
    const verdict = await evaluateExpiration(
      message,
      lazyFull,
      lazyBody,
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
 * Check if a message matches a rule's criteria (sender, subject, folder).
 * Only uses data from the MessageHeader — no expensive API calls needed.
 */
function matchesRule(message, rule) {
  const { match } = rule;

  // Folder filter
  if (match.folders && match.folders.length > 0) {
    if (!match.folders.includes(message.folder?.path)) {
      return false;
    }
  }

  // Sender patterns — match against both the full author string
  // ("Display Name <email@example.com>") and the extracted email address
  if (match.senderPatterns && match.senderPatterns.length > 0) {
    const authorRaw = (message.author || "").toLowerCase();
    const emailOnly = extractEmail(authorRaw);
    const senderMatch = match.senderPatterns.some((pattern) => {
      const p = pattern.toLowerCase();
      return globMatch(authorRaw, p) || (emailOnly && globMatch(emailOnly, p));
    });
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

  // Rules that only have headerMatch and no sender/subject patterns
  // (like the Expires header rule) match all messages — the header check
  // happens in evaluateExpiration where we lazily fetch full headers.

  return true;
}

/**
 * Evaluate whether a message has expired based on the rule's expiration type.
 * Only fetches fullMessage/bodyText when the rule type actually needs it.
 */
async function evaluateExpiration(message, lazyFull, lazyBody, rule, settings) {
  const now = new Date();
  const sentDate = new Date(message.date);

  switch (rule.expiration.type) {
    case "ttl": {
      // TTL only needs the send date — no full message fetch needed
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
      // Need full message to read the Expires header
      const fullMessage = await lazyFull();
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
      const bodyText = await lazyBody();
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
      const fullMessage = await lazyFull();
      const messageIdHeader = getHeader(fullMessage, "Message-ID") || message.headerMessageId;
      if (messageIdHeader) {
        const cached = await getCachedVerdict(messageIdHeader);
        if (cached) {
          if (cached.expired) {
            return { ...cached, rule };
          }
          // Re-evaluate cached future expiration against current time
          if (cached.expiresAt && now > new Date(cached.expiresAt)) {
            return { ...cached, expired: true, rule };
          }
          return null; // Cached as "not expired" (and still not expired)
        }
      }

      // Call LLM
      try {
        const bodyText = await lazyBody();
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

    case "classify": {
      return {
        classified: true,
        rule,
        reason: rule.name,
        confidence: 1.0,
      };
    }

    case "llm-classify": {
      if (settings.llmProvider === "none") return null;

      const fullMessage = await lazyFull();
      const messageIdHeader = getHeader(fullMessage, "Message-ID") || message.headerMessageId;
      if (messageIdHeader) {
        const cached = await getCachedVerdict(messageIdHeader);
        if (cached) {
          if (cached.classified) return { ...cached, rule };
          return null;
        }
      }

      try {
        const bodyText = await lazyBody();
        const snippet = settings.llmMetadataOnly
          ? null
          : (bodyText || "").substring(0, settings.llmMaxSnippetLength);

        const llmResult = await classifyMessageWithLlm({
          sender: message.author,
          subject: message.subject,
          sentDate: sentDate.toISOString(),
          bodySnippet: snippet,
          category: rule.expiration.category || "receipt",
          customPrompt: rule.expiration.prompt || null,
        }, settings);

        if (messageIdHeader) {
          await setCachedVerdict(messageIdHeader, {
            classified: llmResult.matches,
            reason: llmResult.reason,
            confidence: llmResult.confidence,
          });
        }

        if (llmResult.matches && llmResult.confidence >= settings.llmConfidenceThreshold) {
          return {
            classified: true,
            rule,
            reason: `LLM: ${llmResult.reason}`,
            confidence: llmResult.confidence,
          };
        }
      } catch (e) {
        console.error(`[MailReaper] LLM classify failed for message ${message.id}:`, e);
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

/**
 * Extract the email address from a "Display Name <email>" string.
 */
function extractEmail(author) {
  const match = author.match(/<([^>]+)>/);
  return match ? match[1] : author;
}

export { globMatch, getHeader, extractEmail };
