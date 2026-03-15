/**
 * Prompt templates for LLM analysis and rule generation.
 */

/**
 * Build the analysis prompt for determining if a message has expired.
 */
export function buildAnalysisPrompt(messageData) {
  const { sender, subject, sentDate, bodySnippet, customPrompt } = messageData;

  if (customPrompt) {
    // User-defined prompt with variable substitution
    return customPrompt
      .replace("{sender}", sender || "unknown")
      .replace("{subject}", subject || "")
      .replace("{sentDate}", sentDate || "")
      .replace("{now}", new Date().toISOString())
      .replace("{bodySnippet}", bodySnippet || "(no body content provided)");
  }

  const bodySection = bodySnippet
    ? `\nContent snippet:\n---\n${bodySnippet}\n---`
    : "\n(Body content not provided — analyze based on metadata only)";

  return `You are an email expiration classifier. Your job is to determine if an email contains time-sensitive content that has already expired.

Email metadata:
- From: ${sender || "unknown"}
- Subject: ${subject || "(no subject)"}
- Sent: ${sentDate}
- Current time: ${new Date().toISOString()}
${bodySection}

Analyze the email and respond ONLY with a JSON object (no other text):
{
  "isTimeSensitive": true/false,
  "expiresAt": "ISO-8601 datetime string, or null if not determinable",
  "reason": "brief 1-sentence explanation",
  "confidence": 0.0 to 1.0
}

Classification guidelines:
- Flash sales, limited-time offers, coupons with deadlines → extract the expiration date/time. If the email says "ends tonight" or "today only", the expiration is midnight of the send date (in sender's timezone, assume US Eastern if unknown).
- Event invitations/reminders for past events → expired at event time.
- One-time passwords, verification codes → expire 1 hour after send.
- Transit/service alerts (delays, disruptions) → expire 3 hours after send.
- Shipping "out for delivery" notices → expire 24 hours after send.
- Job application deadlines → extract the deadline date.
- Newsletters, digests, informational content → NOT time-sensitive.
- Personal correspondence → NOT time-sensitive.
- Receipts, order confirmations → NOT time-sensitive (user may need for returns).
- If you cannot determine an expiration with reasonable confidence, set isTimeSensitive to false.
- Prefer false negatives over false positives — when in doubt, don't expire.`;
}

/**
 * Build a prompt for generating a rule from example emails.
 */
export function buildRuleGenerationPrompt(examples) {
  const exampleList = examples
    .map(
      (ex, i) =>
        `Example ${i + 1}:
  From: ${ex.sender}
  Subject: ${ex.subject}
  Sent: ${ex.sentDate}${ex.bodySnippet ? `\n  Body preview: ${ex.bodySnippet.substring(0, 300)}` : ""}`
    )
    .join("\n\n");

  return `You are a rule-generation assistant for an email expiration manager. Given example emails that a user considers "expirable" (time-sensitive emails that should be cleaned up after they expire), propose an expiration rule.

${exampleList}

Analyze the examples and respond ONLY with a JSON object:
{
  "name": "Human-readable rule name",
  "senderPatterns": ["glob patterns for From address, e.g. *@example.com"],
  "subjectPatterns": ["glob patterns for Subject line, e.g. *sale*"],
  "expirationType": "ttl or llm",
  "ttlHours": number (only if expirationType is "ttl"),
  "reason": "brief explanation of why this rule makes sense",
  "confidence": 0.0 to 1.0
}

Guidelines:
- Prefer "ttl" (fixed time-to-live) over "llm" when a fixed window makes sense.
- Use the most specific sender pattern that still captures the class of emails.
- Subject patterns should be broad enough to catch variations but specific enough to avoid false positives.
- Use * for wildcards in patterns.
- If the examples are from the same sender, prioritize senderPatterns. If they share subject keywords, prioritize subjectPatterns.
- TTL should reflect how long the content is useful (transit alerts: 2-3h, sales: until deadline, delivery notices: 24-72h).`;
}
