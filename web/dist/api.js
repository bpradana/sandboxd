// Thin client for the sandboxd REST API (same origin).

const KEY_STORAGE = "sandboxd.apiKey";

export function getAPIKey() {
  try {
    return localStorage.getItem(KEY_STORAGE) || "";
  } catch {
    return "";
  }
}

export function setAPIKey(key) {
  try {
    if (key) localStorage.setItem(KEY_STORAGE, key);
    else localStorage.removeItem(KEY_STORAGE);
  } catch {
    // Storage unavailable (private mode): the key lasts for this page only.
  }
  sessionKey = key;
}

let sessionKey = getAPIKey();

export class APIError extends Error {
  constructor(status, message, code) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

// api calls /api/v1<path> and returns the envelope's data.
export async function api(method, path, body) {
  const headers = { Accept: "application/json" };
  if (sessionKey) headers.Authorization = `Bearer ${sessionKey}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";

  let response;
  try {
    response = await fetch(`/api/v1${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new APIError(0, "cannot reach the sandboxd API");
  }

  let envelope = null;
  try {
    envelope = await response.json();
  } catch {
    // Non-JSON body (e.g. a proxy error page).
  }
  if (!response.ok) {
    const detail = envelope?.errors ? Object.values(envelope.errors).flat().join("; ") : "";
    const message = detail || envelope?.error || envelope?.message || `${response.status} ${response.statusText}`;
    throw new APIError(response.status, message, envelope?.error_code);
  }
  return envelope?.data;
}

export async function engineReady() {
  try {
    const response = await fetch("/readyz", { headers: { Accept: "application/json" } });
    return response.ok;
  } catch {
    return false;
  }
}
