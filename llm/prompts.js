/**
 * Prompt templates for LLM analysis and rule generation.
 */

/**
 * Build the analysis prompt for determining if a message has expired.
 */
export function buildAnalysisPrompt(messageData, examples) {
  const { sender, subject, sentDate, bodySnippet, customPrompt } = messageData;

  const bodySection = bodySnippet
    ? `\nContent snippet:\n---\n${bodySnippet}\n---`
    : "\n(Body content not provided — analyze based on metadata only)";

  const customSection = customPrompt
    ? `\n\nAdditional user-provided guidelines (these take priority):\n${customPrompt}`
    : "";

  // Include user-confirmed expired examples as few-shot references (up to 5 most recent)
  let examplesSection = "";
  if (examples && examples.length > 0) {
    const recent = examples.slice(-5);
    const lines = recent.map((ex, i) => {
      let line = `${i + 1}. From: ${ex.sender} | Subject: ${ex.subject} | Sent: ${ex.sentDate}`;
      if (ex.expiresAt) line += ` | Expired at: ${ex.expiresAt}`;
      return line;
    });
    examplesSection = `\n\nThe user has confirmed these emails were time-sensitive and expired:\n${lines.join("\n")}\n\nUse these as reference — similar emails should be treated as time-sensitive.`;
  }

  const currentTime = new Date().toISOString();

  return `You are an email expiration classifier. Determine if this email is time-sensitive and whether it has expired.

IMPORTANT: The current date/time is ${currentTime}. Compare any dates in the email against this.${examplesSection}

Email:
- From: ${sender || "unknown"}
- Subject: ${subject || "(no subject)"}
- Sent: ${sentDate}
${bodySection}

Respond ONLY with JSON:
{
  "isTimeSensitive": true/false,
  "expiresAt": "ISO-8601 datetime or null",
  "reason": "brief explanation",
  "confidence": 0.0 to 1.0
}

Rules for what is time-sensitive (set isTimeSensitive to true and extract expiresAt):
- Appointment reminders, event reminders, calendar notifications → expire at the event time. "Reminder: Upcoming appointment" for March 12 is expired if current date is after March 12.
- Flash sales, coupons, limited-time offers → expire at the deadline. "Today only" or "ends tonight" = midnight of send date (assume US Eastern).
- One-time passwords, verification codes, magic links → expire 1 hour after send.
- Transit/service alerts, delays, disruptions → expire 3 hours after send.
- Shipping "out for delivery" or "arriving today" notices → expire 24 hours after send.
- Event invitations for past dates → expire at event time.
- Job application deadlines → expire at the deadline.
- Check-in reminders (flights, hotels, medical) → expire 24 hours after the check-in time.

NOT time-sensitive (set isTimeSensitive to false):
- Newsletters, digests, informational content.
- Personal correspondence.
- Receipts, order confirmations, billing statements.

If the email references a specific date/time that is before ${currentTime}, it is expired. When in doubt, prefer false negatives.${customSection}`;
}

/**
 * Build the classification prompt for categorizing an email.
 */
export function buildClassificationPrompt(messageData, examples) {
  const { sender, subject, sentDate, bodySnippet, category, customPrompt } = messageData;

  const bodySection = bodySnippet
    ? `\nContent snippet:\n---\n${bodySnippet}\n---`
    : "\n(Body content not provided — analyze based on metadata only)";

  const categoryDescriptions = {
    receipt: "a purchase receipt, payment confirmation, order confirmation, paid invoice, billing statement, or financial transaction record",
  };

  const desc = categoryDescriptions[category] || category;

  const customSection = customPrompt
    ? `\n\nAdditional user-provided guidelines (these take priority):\n${customPrompt}`
    : "";

  // Include user-confirmed examples as few-shot references (up to 5 most recent)
  let examplesSection = "";
  if (examples && examples.length > 0) {
    const recent = examples.slice(-5);
    const lines = recent.map(
      (ex, i) => `${i + 1}. From: ${ex.sender} | Subject: ${ex.subject}`
    );
    examplesSection = `\n\nThe user has confirmed these emails are ${category}s:\n${lines.join("\n")}\n\nUse these as reference when classifying the email below.`;
  }

  return `You are an email classifier. Determine if this email is ${desc}.${examplesSection}

Email metadata:
- From: ${sender || "unknown"}
- Subject: ${subject || "(no subject)"}
- Sent: ${sentDate}
${bodySection}

Respond ONLY with a JSON object:
{
  "matches": true/false,
  "reason": "brief 1-sentence explanation",
  "confidence": 0.0 to 1.0
}

Guidelines:
- Purchase receipts, order confirmations, payment confirmations → matches
- Monthly/annual billing statements, subscription renewals → matches
- Paid invoices, donation receipts, tax documents → matches
- Invoices requesting payment (unpaid, due, amount owed) → does NOT match (user needs to act on these)
- Invoices where payment status is unclear → does NOT match (assume unpaid)
- Shipping/delivery notifications → does NOT match (tracked separately)
- Marketing emails from stores → does NOT match
- Account alerts, password resets → does NOT match
- Prefer false negatives over false positives — when in doubt, say false.${customSection}`;
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
