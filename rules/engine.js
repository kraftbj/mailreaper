/**
 * Rule Engine — evaluates messages against rules to determine expiration.
 *
 * Returns a verdict for each message:
 * { expired, rule, expiresAt, reason, confidence, classified, noMatch, hasLlmError, trace }
 */

import { getRules, getSettings, getCachedVerdict, setCachedVerdict, getTrainingExamples } from "./storage.js";
import { analyzeMessageWithLlm, classifyMessageWithLlm } from "../llm/adapter.js";

/**
 * Evaluate a single message against all enabled rules (in priority order).
 * Returns the first matching verdict, or `{ noMatch: true, trace }` if no rule matches.
 *
 * fullMessage and bodyText are fetched lazily — only when a rule needs them.
 *
 * @param {object} message - Thunderbird MessageHeader object
 * @param {function} getFullMessage - async function returning messages.getFull() result
 * @param {function} getBodyText - async function returning plain text body
 * @returns {Promise<object>} Verdict object (with noMatch: true if no rule matched)
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

  // Track which rules were evaluated for debugging
  const trace = [];
  let hasLlmError = false;

  for (const rule of enabledRules) {
    // Check if message matches this rule's criteria (uses only MessageHeader data)
    if (!matchesRule(message, rule)) {
      continue;
    }

    trace.push(rule.name);

    // Evaluate expiration based on rule type
    const verdict = await evaluateExpiration(
      message,
      lazyFull,
      lazyBody,
      rule,
      settings
    );

    if (verdict && verdict.skippedTrace) {
      trace.push(verdict.skippedTrace);
      continue;
    }

    if (verdict && verdict.llmError) {
      hasLlmError = true;
      continue;
    }

    if (verdict) {
      verdict.trace = trace;
      return verdict;
    }
  }

  return { noMatch: true, hasLlmError, trace };
}

/**
 * Check if a message matches a rule's static criteria (sender, subject, folder).
 * Header-level matching (headerMatch) is deferred to evaluateExpiration().
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

  // Rules with no sender/subject/folder patterns (like the Expires header
  // rule) match all messages — the header check happens in evaluateExpiration
  // where we lazily fetch full headers.

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
      // Early-exit: fetch headers and bail immediately if no Expires header.
      // The builtin Expires rule has no sender/subject patterns, so it matches
      // every message in matchesRule(). This check avoids wasted work for the
      // vast majority of messages that lack the header.
      const fullMessage = await lazyFull();
      const expiresHeader = getHeader(fullMessage, "Expires");
      if (!expiresHeader) return null;

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
      return null;
    }

    case "content-regex": {
      const bodyText = await lazyBody();
      if (!bodyText || !rule.expiration.pattern) return null;

      try {
        const regex = new RegExp(rule.expiration.pattern, "i");
        // Truncate body to limit ReDoS exposure
        const truncatedBody = bodyText.length > 50000 ? bodyText.substring(0, 50000) : bodyText;
        const match = truncatedBody.match(regex);
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
        return { skippedTrace: "Regex error: " + e.message };
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
          if (cached.error) return { llmError: true };
          // Always check confidence threshold on cache hits — user may have raised it
          const threshold = settings.llmConfidenceThreshold ?? 0.7;
          if (cached.expired) {
            if ((cached.confidence ?? 0) < threshold) {
              return { skippedTrace: `Cached LLM verdict below confidence threshold (${cached.confidence} < ${threshold})` };
            }
            return { ...cached, rule };
          }
          // Re-evaluate cached future expiration against current time
          if (cached.expiresAt && now > new Date(cached.expiresAt)) {
            if ((cached.confidence ?? 0) < threshold) {
              return { skippedTrace: `Cached LLM verdict expired but below confidence threshold (${cached.confidence} < ${threshold})` };
            }
            return { ...cached, expired: true, rule, reason: cached.reason || "Cached expiration date passed" };
          }
          return null; // Cached as "not expired" (and still not expired)
        }
      }

      // Call LLM
      try {
        const bodyText = await lazyBody();
        if (bodyText === null) {
          return { skippedTrace: "Skipped LLM: body text unavailable" };
        }
        const snippet = settings.llmMetadataOnly
          ? null
          : (bodyText || "").substring(0, settings.llmMaxSnippetLength);

        const expiryExamples = await getTrainingExamples("expiry");

        const llmResult = await analyzeMessageWithLlm({
          sender: message.author,
          subject: message.subject,
          sentDate: sentDate.toISOString(),
          bodySnippet: snippet,
          customPrompt: rule.expiration.prompt || null,
        }, settings, expiryExamples);

        console.log(`[MailReaper] LLM result for "${message.subject}":`, JSON.stringify(llmResult));

        const isExpired = llmResult.isTimeSensitive && llmResult.expiresAt && now > new Date(llmResult.expiresAt);
        const meetsThreshold = (llmResult.confidence ?? 0) >= (settings.llmConfidenceThreshold ?? 0.7);

        // Cache the result — only mark expired if confidence meets threshold
        if (messageIdHeader) {
          await setCachedVerdict(messageIdHeader, {
            expired: isExpired && meetsThreshold,
            expiresAt: llmResult.expiresAt,
            reason: llmResult.reason,
            confidence: llmResult.confidence,
          });
        }

        if (llmResult.isTimeSensitive && llmResult.expiresAt) {
          const expiresAt = new Date(llmResult.expiresAt);
          if (isNaN(expiresAt.getTime())) {
            console.warn(`[MailReaper] LLM returned unparseable expiresAt: "${llmResult.expiresAt}" for "${message.subject}"`);
          } else if (llmResult.confidence < settings.llmConfidenceThreshold) {
            const detail = llmResult.confidence === 0.5
              ? `LLM verdict below confidence threshold (0.5 imputed, needs ${settings.llmConfidenceThreshold})`
              : `LLM verdict below confidence threshold (${llmResult.confidence}, needs ${settings.llmConfidenceThreshold})`;
            console.log(`[MailReaper] ${detail} for "${message.subject}"`);
            return { skippedTrace: detail };
          } else if (now > expiresAt) {
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
        // Cache the error so getMessageInfo can report it
        if (messageIdHeader) {
          await setCachedVerdict(messageIdHeader, {
            error: e.message || "LLM call failed",
            reason: `Error: ${e.message || "LLM call failed"}`,
          });
        }
        return { llmError: true };
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
          if (cached.error) return { llmError: true };
          if (cached.classified) {
            const threshold = settings.llmConfidenceThreshold ?? 0.7;
            if ((cached.confidence ?? 0) < threshold) {
              return null; // Below current threshold
            }
            return { ...cached, rule };
          }
          return null;
        }
      }

      try {
        const bodyText = await lazyBody();
        if (bodyText === null) {
          return { skippedTrace: "Skipped LLM: body text unavailable" };
        }
        const snippet = settings.llmMetadataOnly
          ? null
          : (bodyText || "").substring(0, settings.llmMaxSnippetLength);

        const category = rule.expiration.category || "receipt";
        const trainingExamples = await getTrainingExamples(category);

        const llmResult = await classifyMessageWithLlm({
          sender: message.author,
          subject: message.subject,
          sentDate: sentDate.toISOString(),
          bodySnippet: snippet,
          category,
          customPrompt: rule.expiration.prompt || null,
        }, settings, trainingExamples);

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
        if (messageIdHeader) {
          await setCachedVerdict(messageIdHeader, {
            error: e.message || "LLM classify failed",
            reason: `Error: ${e.message || "LLM classify failed"}`,
          });
        }
        return { llmError: true };
      }
      return null;
    }

    default:
      console.warn(`[MailReaper] Unknown expiration type: ${rule.expiration.type}`);
      return { skippedTrace: "Unknown expiration type: " + rule.expiration.type };
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
 * Patterns are anchored — must match the entire string.
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
  } catch (e) {
    console.warn(`[MailReaper] Invalid glob pattern "${pattern}":`, e);
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
