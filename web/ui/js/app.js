/**
 * app.js — shared utilities for all MailReaper UI pages.
 */

/**
 * Fetch wrapper for /api/* endpoints. Adds JSON headers and throws on
 * non-2xx responses with the server's error message when available.
 *
 * @param {string} path - API path (e.g. "/api/stats")
 * @param {RequestInit} [options]
 * @returns {Promise<any>} Parsed JSON response body
 */
export async function api(path, options = {}) {
  const headers = {
    "Content-Type": "application/json",
    "Accept": "application/json",
    ...(options.headers || {}),
  };

  const res = await fetch(path, { ...options, headers });

  if (!res.ok) {
    let msg = `API error ${res.status}`;
    try {
      const body = await res.json();
      if (body.error) msg = body.error;
    } catch (_) {
      // ignore parse error
    }
    throw new Error(msg);
  }

  // 204 No Content
  if (res.status === 204) return null;

  return res.json();
}

/**
 * Opens a Server-Sent Events connection to /api/events and calls onEvent for
 * each received event. Returns a function that closes the connection.
 *
 * @param {function({type: string, data: any}): void} onEvent
 * @returns {function(): void} close function
 */
export function connectSSE(onEvent) {
  const es = new EventSource("/api/events");

  es.onmessage = (e) => {
    try {
      const data = JSON.parse(e.data);
      onEvent({ type: e.type || "message", data });
    } catch (_) {
      onEvent({ type: "message", data: e.data });
    }
  };

  es.addEventListener("scan_complete", (e) => {
    try {
      onEvent({ type: "scan_complete", data: JSON.parse(e.data) });
    } catch (_) {
      onEvent({ type: "scan_complete", data: {} });
    }
  });

  es.onerror = () => {
    // EventSource reconnects automatically; no action needed.
  };

  return () => es.close();
}

/**
 * Returns a human-readable relative time string for a date string or Date.
 * Examples: "just now", "3 minutes ago", "2 hours ago", "yesterday", "5 days ago"
 *
 * @param {string|Date} dateStr
 * @returns {string}
 */
export function timeAgo(dateStr) {
  if (!dateStr) return "";
  const date = typeof dateStr === "string" ? new Date(dateStr) : dateStr;
  const now = Date.now();
  const diff = now - date.getTime();

  if (diff < 0) return "just now";
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) {
    const m = Math.floor(diff / 60_000);
    return `${m} minute${m === 1 ? "" : "s"} ago`;
  }
  if (diff < 86_400_000) {
    const h = Math.floor(diff / 3_600_000);
    return `${h} hour${h === 1 ? "" : "s"} ago`;
  }
  if (diff < 172_800_000) return "yesterday";
  const d = Math.floor(diff / 86_400_000);
  return `${d} days ago`;
}

/**
 * Formats a confidence value (0–1) as a percentage string.
 *
 * @param {number} conf
 * @returns {string}
 */
export function fmtConfidence(conf) {
  if (conf == null) return "—";
  return `${Math.round(conf * 100)}%`;
}

/**
 * Returns the CSS class for an activity type badge.
 *
 * @param {string} type
 * @returns {string}
 */
export function badgeClass(type) {
  const map = {
    expired: "badge-expired",
    corrected: "badge-corrected",
    pending: "badge-pending",
    classify: "badge-classify",
  };
  return map[type] || "badge-pending";
}
