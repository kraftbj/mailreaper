/**
 * LLM Adapter — abstraction over Gemini and Ollama backends.
 * Handles prompt construction, API calls, and response parsing.
 */

import { buildAnalysisPrompt, buildRuleGenerationPrompt, buildClassificationPrompt } from "./prompts.js";

/**
 * Analyze a message for time-sensitive expiration using the configured LLM.
 *
 * @param {object} messageData - { sender, subject, sentDate, bodySnippet, customPrompt }
 * @param {object} settings - Extension settings (provider, keys, endpoints)
 * @param {Array<object>} examples - Training examples for few-shot prompting
 * @returns {Promise<object>} { isTimeSensitive, expiresAt, reason, confidence }
 */
export async function analyzeMessageWithLlm(messageData, settings, examples) {
  const prompt = buildAnalysisPrompt(messageData, examples);

  switch (settings.llmProvider) {
    case "gemini":
      return callGemini(prompt, settings);
    case "ollama":
      return callOllama(prompt, settings);
    default:
      throw new Error(`Unknown LLM provider: ${settings.llmProvider}`);
  }
}

/**
 * Classify a message using the configured LLM.
 *
 * @param {object} messageData - { sender, subject, sentDate, bodySnippet, category, customPrompt }
 * @param {object} settings - Extension settings
 * @param {Array<object>} examples - Training examples for few-shot prompting
 * @returns {Promise<object>} { matches, reason, confidence }
 */
export async function classifyMessageWithLlm(messageData, settings, examples) {
  const prompt = buildClassificationPrompt(messageData, examples);

  switch (settings.llmProvider) {
    case "gemini":
      return callGemini(prompt, settings);
    case "ollama":
      return callOllama(prompt, settings);
    default:
      throw new Error(`Unknown LLM provider: ${settings.llmProvider}`);
  }
}

/**
 * Ask the LLM to generate a rule from example emails.
 *
 * @param {Array<object>} examples - Array of { sender, subject, sentDate, bodySnippet }
 * @param {object} settings - Extension settings
 * @returns {Promise<object>} Proposed rule object
 */
export async function generateRuleFromExamples(examples, settings) {
  const prompt = buildRuleGenerationPrompt(examples);

  let result;
  switch (settings.llmProvider) {
    case "gemini":
      result = await callGemini(prompt, settings);
      break;
    case "ollama":
      result = await callOllama(prompt, settings);
      break;
    default:
      throw new Error(`LLM provider not configured`);
  }

  return result;
}

/**
 * Test LLM connection with a simple prompt.
 */
export async function testLlmConnection(settings) {
  const testPrompt = 'Respond with exactly: {"status": "ok"}';

  try {
    switch (settings.llmProvider) {
      case "gemini":
        await callGemini(testPrompt, settings);
        return { success: true };
      case "ollama":
        await callOllama(testPrompt, settings);
        return { success: true };
      default:
        return { success: false, error: "No LLM provider selected" };
    }
  } catch (e) {
    return { success: false, error: e.message };
  }
}

// ── Gemini Backend ──────────────────────────────────────────────────────────

async function callGemini(prompt, settings) {
  const { geminiApiKey, geminiModel } = settings;

  if (!geminiApiKey) {
    throw new Error("Gemini API key not configured");
  }

  const endpoint = `https://generativelanguage.googleapis.com/v1beta/models/${geminiModel}:generateContent`;

  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 30000);

  try {
    const response = await fetch(endpoint, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "x-goog-api-key": geminiApiKey,
      },
      signal: controller.signal,
      body: JSON.stringify({
        contents: [
          {
            parts: [{ text: prompt }],
          },
        ],
        generationConfig: {
          temperature: 0.1,
          maxOutputTokens: 512,
          responseMimeType: "application/json",
        },
      }),
    });

    if (!response.ok) {
      const errBody = await response.text();
      throw new Error(`Gemini API error (${response.status}): ${errBody}`);
    }

    const data = await response.json();
    const candidate = data.candidates?.[0];
    if (candidate?.finishReason && candidate.finishReason !== "STOP") {
      throw new Error(`Gemini refused to respond (reason: ${candidate.finishReason})`);
    }
    const text = candidate?.content?.parts?.[0]?.text;

    if (!text) {
      throw new Error("Empty response from Gemini");
    }

    return parseJsonResponse(text);
  } finally {
    clearTimeout(timeout);
  }
}

// ── Ollama Backend ──────────────────────────────────────────────────────────

async function callOllama(prompt, settings) {
  const { ollamaEndpoint, ollamaModel } = settings;

  if (!ollamaEndpoint) {
    throw new Error("Ollama endpoint not configured");
  }

  const endpoint = `${ollamaEndpoint}/api/generate`;

  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 30000);

  try {
    const response = await fetch(endpoint, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      signal: controller.signal,
      body: JSON.stringify({
        model: ollamaModel,
        prompt: prompt,
        stream: false,
        format: "json",
        options: {
          temperature: 0.1,
          num_predict: 512,
        },
      }),
    });

    if (!response.ok) {
      const errBody = await response.text();
      throw new Error(`Ollama error (${response.status}): ${errBody}`);
    }

    const data = await response.json();
    if (!data.response) {
      throw new Error("Empty response from Ollama");
    }
    return parseJsonResponse(data.response);
  } finally {
    clearTimeout(timeout);
  }
}

// ── Response Parsing ────────────────────────────────────────────────────────

function parseJsonResponse(text) {
  // Strip markdown code fences if present
  const cleaned = text
    .replace(/```json\s*/gi, "")
    .replace(/```\s*/g, "")
    .trim();

  try {
    const parsed = JSON.parse(cleaned);

    // Validate and normalize the response shape
    if (parsed.confidence === undefined && parsed.score === undefined) {
      console.warn("[MailReaper] LLM response missing confidence field, defaulting to 0.5");
    }
    const rawConfidence = Number(parsed.confidence ?? parsed.score ?? 0.5);
    const confidence = Number.isNaN(rawConfidence) ? 0.5 : Math.max(0, Math.min(1, rawConfidence));

    const rawExpiresAt = parsed.expiresAt ?? parsed.expires_at ?? null;
    const parsedDate = rawExpiresAt != null ? new Date(rawExpiresAt) : null;
    const expiresAt = parsedDate && !Number.isNaN(parsedDate.getTime()) ? rawExpiresAt : null;

    return {
      isTimeSensitive: !!(parsed.isTimeSensitive ?? parsed.is_time_sensitive ?? false),
      expiresAt,
      reason: String(parsed.reason ?? parsed.explanation ?? "No reason given"),
      confidence,
      // For classification responses
      ...(parsed.matches !== undefined && { matches: Boolean(parsed.matches) }),
      // For rule generation responses
      ...(parsed.name && { name: parsed.name }),
      ...(parsed.senderPatterns && { senderPatterns: parsed.senderPatterns }),
      ...(parsed.subjectPatterns && { subjectPatterns: parsed.subjectPatterns }),
      ...(parsed.ttlHours && { ttlHours: parsed.ttlHours }),
      ...(parsed.expirationType && { expirationType: parsed.expirationType }),
    };
  } catch (e) {
    console.error("[MailReaper] Failed to parse LLM JSON response:", cleaned);
    throw new Error(`Invalid JSON from LLM: ${e.message}`);
  }
}
