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

// Raw file transfers use the same authentication and error handling as JSON.
export async function request(method, path, { body, contentType, signal } = {}) {
  const headers = { Accept: "*/*" };
  if (sessionKey) headers.Authorization = `Bearer ${sessionKey}`;
  if (contentType) headers["Content-Type"] = contentType;

  let response;
  try {
    response = await fetch(`/api/v1${path}`, {
      method,
      headers,
      body,
      signal,
    });
  } catch (error) {
    if (error.name === "AbortError") throw error;
    throw new APIError(0, "cannot reach the sandboxd API");
  }
  if (response.ok) return response;
  let envelope = null;
  try {
    envelope = await response.json();
  } catch {
    // Non-JSON body (e.g. a proxy error page).
  }
  const detail = envelope?.errors ? Object.values(envelope.errors).flat().join("; ") : "";
  const message = detail || envelope?.error || envelope?.message || `${response.status} ${response.statusText}`;
  throw new APIError(response.status, message, envelope?.error_code);
}

export async function api(method, path, body, options = {}) {
  const response = await request(method, path, { body: body === undefined ? undefined : JSON.stringify(body),
    contentType: body === undefined ? undefined : "application/json", signal: options.signal });
  try { return (await response.json())?.data; } catch { return undefined; }
}

export async function engineReady() {
  try {
    const response = await fetch("/readyz", { headers: { Accept: "application/json" } });
    return response.ok;
  } catch {
    return false;
  }
}
