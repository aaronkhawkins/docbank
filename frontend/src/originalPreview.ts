import { APIError, type ContentVersion, type Node } from "./api.js";
import { prepareVersionDownload, type DownloadProgress } from "./download.js";

export const MAX_INLINE_PREVIEW_BYTES = 25 * 1024 * 1024;

export type OriginalPreviewKind = "image" | "pdf";

const SAFE_RASTER_TYPES = new Set([
  "image/avif",
  "image/gif",
  "image/jpeg",
  "image/png",
  "image/webp",
]);

function normalizedMimeType(mimeType: string | undefined): string {
  return (mimeType ?? "").trim().toLowerCase().split(";", 1)[0];
}

export function originalPreviewKind(
  mimeType: string | undefined,
  size: number,
): OriginalPreviewKind | null {
  if (!Number.isSafeInteger(size) || size < 0 || size > MAX_INLINE_PREVIEW_BYTES) {
    return null;
  }
  const normalized = normalizedMimeType(mimeType);
  if (normalized === "application/pdf") return "pdf";
  return SAFE_RASTER_TYPES.has(normalized) ? "image" : null;
}

export async function loadOriginalPreview(
  session: string,
  node: Node,
  version: ContentVersion,
  signal: AbortSignal,
  onprogress: (progress: DownloadProgress) => void,
): Promise<Blob> {
  const kind = originalPreviewKind(version.mime_type, version.size);
  if (!kind) throw new Error("This original is not eligible for inline preview.");

  const prepared = await prepareVersionDownload(session, node, version, signal, onprogress);
  const response = await fetch(prepared.url, {
    signal,
    credentials: "same-origin",
    cache: "no-store",
  });
  try {
    if (!response.ok) {
      if (response.status === 401) {
        throw new APIError("The document session expired.", response.status, "unauthorized");
      }
      throw new Error(`The verified original could not be opened (HTTP ${response.status}).`);
    }
    if (
      response.headers.get("X-Docbank-Content-Version") !== prepared.versionID ||
      response.headers.get("X-Docbank-Blob-Hash") !== prepared.blobHash ||
      response.headers.get("X-Docbank-Blob-Size") !== String(prepared.size)
    ) {
      throw new Error("The preview response disagreed with the selected document.");
    }
    const selectedMimeType = normalizedMimeType(version.mime_type);
    if (normalizedMimeType(response.headers.get("Content-Type") ?? "") !== selectedMimeType) {
      throw new Error("The preview MIME type disagreed with the selected document.");
    }

    const responseBlob = await response.blob();
    const blob = responseBlob.slice(0, responseBlob.size, selectedMimeType);
    if (blob.size !== prepared.size) {
      throw new Error("The preview bytes disagreed with the selected document.");
    }
    if (signal.aborted) throw new DOMException("The preview was cancelled.", "AbortError");
    return blob;
  } catch (cause) {
    await response.body?.cancel().catch(() => undefined);
    throw cause;
  }
}
