import { afterEach, describe, expect, it, vi } from "vitest";
import type { ContentVersion, Node } from "./api.js";
import {
  loadOriginalPreview,
  MAX_INLINE_PREVIEW_BYTES,
  originalPreviewKind,
} from "./originalPreview.js";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("originalPreviewKind", () => {
  it.each([
    ["application/pdf", "pdf"],
    ["image/jpeg", "image"],
    ["image/png", "image"],
    ["image/gif", "image"],
    ["image/webp", "image"],
    ["image/avif", "image"],
  ] as const)("allows %s originals", (mimeType, kind) => {
    expect(originalPreviewKind(mimeType, MAX_INLINE_PREVIEW_BYTES)).toBe(kind);
  });

  it.each([undefined, "", "image/svg+xml", "text/html", "text/plain", "application/xhtml+xml"])(
    "rejects active or unsupported type %s",
    (mimeType) => {
      expect(originalPreviewKind(mimeType, 1024)).toBeNull();
    },
  );

  it("rejects originals above the inline size budget", () => {
    expect(originalPreviewKind("application/pdf", MAX_INLINE_PREVIEW_BYTES + 1)).toBeNull();
  });

  it.each([
    ["Content-Type", "text/html", "MIME type"],
    ["X-Docbank-Content-Version", "22222222-2222-4222-8222-222222222222", "selected document"],
    ["X-Docbank-Blob-Hash", "b".repeat(64), "selected document"],
    ["X-Docbank-Blob-Size", "3", "selected document"],
  ])("rejects fetched bytes with mismatched %s", async (header, value, message) => {
    const node = {
      id: 42,
      name: "safe.pdf",
      kind: "file",
      revision: 2,
    } as Node;
    const version = {
      id: "11111111-1111-4111-8111-111111111111",
      node_id: 42,
      blob_hash: "a".repeat(64),
      size: 4,
      mime_type: "application/pdf",
    } as ContentVersion;
    const createObjectURL = vi.fn().mockReturnValue("blob:unsafe");
    vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL: vi.fn() });
    const unsafeResponse = new Response("html", {
      headers: {
        "Content-Type": "application/pdf",
        "X-Docbank-Content-Version": version.id,
        "X-Docbank-Blob-Hash": version.blob_hash,
        "X-Docbank-Blob-Size": "4",
        [header]: value,
      },
    });
    const cancel = vi.spyOn(unsafeResponse.body!, "cancel");
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(
          `${JSON.stringify({
            phase: "ready",
            received: 4,
            total: 4,
            url: "/api/daemon/web-download/file?ticket=preview",
            name: "safe.pdf",
            version_id: version.id,
            blob_hash: version.blob_hash,
          })}\n`,
        ),
      )
      .mockResolvedValueOnce(unsafeResponse);

    await expect(
      loadOriginalPreview(
        "bounded",
        node,
        version,
        new AbortController().signal,
        () => undefined,
      ),
    ).rejects.toThrow(message);
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(cancel).toHaveBeenCalledOnce();
  });
});
