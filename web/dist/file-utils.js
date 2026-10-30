export const EDIT_LIMIT = 1024 * 1024;
export const DOWNLOAD_LIMIT = 256 * 1024 * 1024;

export function cleanPath(path) {
  if (!path.startsWith("/") || path.includes("\0")) throw new Error("Enter an absolute path inside the sandbox.");
  const parts = [];
  for (const part of path.split("/")) {
    if (part === "..") parts.pop();
    else if (part && part !== ".") parts.push(part);
  }
  return "/" + parts.join("/");
}

export function childPath(directory, name) {
  if (!name || name === "." || name === ".." || name.includes("/") || name.includes("\0") || new TextEncoder().encode(name).length > 255) throw new Error("Enter a file or folder name without slashes (up to 255 bytes).");
  return cleanPath(`${directory}/${name}`);
}

export function fileURL(id, path, operation = "", extra = {}) {
  const query = new URLSearchParams({ path: cleanPath(path), ...extra });
  return `/sandboxes/${encodeURIComponent(id)}/files${operation ? "/" + operation : ""}?${query}`;
}

export function writeMode(mode = "0644") {
  return ((parseInt(mode, 8) || 0) & 0o1777).toString(8).padStart(4, "0");
}

export function sameBytes(a, b) { return a.length === b.length && a.every((byte, i) => byte === b[i]); }

export function decodeText(bytes) {
  if (bytes.includes(0)) throw new Error("Binary file. Use Download to save it.");
  let text;
  try { text = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes); }
  catch { throw new Error("This file is not UTF-8 text. Use Download to save it."); }
  const crlf = text.includes("\r\n") && !text.replaceAll("\r\n", "").includes("\n");
  return { text: text.replaceAll("\r\n", "\n").replaceAll("\r", "\n"), crlf };
}

export async function readBytes(response, limit) {
  const tooLarge = () => new Error(`File exceeds the ${Math.round(limit / (1024 * 1024))} MiB browser limit.`);
  if (Number(response.headers.get("Content-Length")) > limit) { await response.body?.cancel(); throw tooLarge(); }
  const reader = response.body?.getReader();
  if (!reader) return new Uint8Array();
  const chunks = [];
  let length = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      length += value.length;
      if (length > limit) { await reader.cancel(); throw tooLarge(); }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const bytes = new Uint8Array(length);
  let position = 0;
  for (const chunk of chunks) { bytes.set(chunk, position); position += chunk.length; }
  return bytes;
}
