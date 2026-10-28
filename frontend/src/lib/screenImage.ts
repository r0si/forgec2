export function dataUrlToBlob(dataUrl: string): Blob | null {
  if (dataUrl.startsWith("blob:")) return null;
  const comma = dataUrl.indexOf(",");
  if (comma === -1) return null;
  const meta = dataUrl.slice(0, comma);
  const payload = dataUrl.slice(comma + 1);
  const mime = meta.match(/data:([^;]+)/)?.[1] || "image/png";
  // A data URL is base64 ONLY when its media type carries ";base64" (RFC 2397). Without that marker
  // the payload is percent-encoded text, and running atob() on it produced garbage bytes for any
  // non-base64 URL (data:text/plain,test became three meaningless bytes) instead of the text.
  const isBase64 = /;base64(;|$)/i.test(meta);
  try {
    const text = isBase64 ? atob(payload) : decodeURIComponent(payload);
    const bytes = new Uint8Array(text.length);
    for (let i = 0; i < text.length; i++) bytes[i] = text.charCodeAt(i) & 0xff;
    return new Blob([bytes], { type: mime });
  } catch {
    return null;
  }
}

export function dataUrlToBlobUrl(dataUrl: string): string {
  if (dataUrl.startsWith("blob:")) return dataUrl;
  const blob = dataUrlToBlob(dataUrl);
  if (!blob) return dataUrl;
  return URL.createObjectURL(blob);
}

export function revokeBlobUrl(url: string) {
  if (url.startsWith("blob:")) {
    try { URL.revokeObjectURL(url); } catch { void 0; }
  }
}
