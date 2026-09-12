import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import DocumentViewer from "./DocumentViewer.svelte";

afterEach(() => {
  cleanup();
  history.replaceState(null, "", "/");
  vi.restoreAllMocks();
});

const versionID = "11111111-1111-4111-8111-111111111111";
const buildID = "b".repeat(64);
const sourceHash = "a".repeat(64);

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

function viewer(offset: number) {
  return {
    node: {
      id: 42,
      name: "registration.pdf",
      kind: "file",
      current_version_id: "22222222-2222-4222-8222-222222222222",
      blob_hash: "c".repeat(64),
      size: 99,
      revision: 2,
      created_at: "2026-07-27T12:00:00Z",
      modified_at: "2026-07-28T12:00:00Z",
      path: "/Records/registration.pdf",
    },
    version: {
      id: versionID,
      node_id: 42,
      blob_hash: sourceHash,
      size: 2048,
      mime_type: "application/pdf",
      recorded_at: "2026-07-27T12:00:00Z",
      node_revision: 1,
      introduced_operation_id: "33333333-3333-4333-8333-333333333333",
      transition_kind: "content_create",
    },
    rendition: {
      build_id: buildID,
      source_sha256: sourceHash,
      evidence_checksum: "e".repeat(64),
      completeness: "complete",
      build_truncated: false,
      warnings: [],
      published_at: "2026-07-27T12:05:00Z",
      segments: [
        {
          id: `segment-${offset}`,
          unit_id: "page-1",
          order: offset,
          char_start: offset,
          char_end: offset + 20,
          checksum: "d".repeat(64),
          text: offset === 0 ? "Synthetic registration transcript" : "Second page",
        },
      ],
      total: 2,
      limit: 100,
      offset,
    },
  };
}

it("opens a pinned historical original and its OCR through an in-memory session", async () => {
  history.replaceState(null, "", `/documents/42/versions/${versionID}`);
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    if (path === "/api/daemon/web-session") {
      return json({ token: "bounded", upload_secret: "unused", url: "http://127.0.0.1/private" }, 201);
    }
    if (path.includes("offset=0")) return json(viewer(0));
    if (path.includes("offset=1") && path.includes(`build_id=${buildID}`)) {
      return json(viewer(1));
    }
    throw new Error(`unexpected request ${path}`);
  });

  render(DocumentViewer);

  expect(await screen.findByRole("heading", { name: "registration.pdf" })).toBeTruthy();
  expect(screen.getByText("Historical")).toBeTruthy();
  expect(screen.getByText(versionID)).toBeTruthy();
  expect(screen.getByText("Synthetic registration transcript")).toBeTruthy();
  expect(screen.getByRole("button", { name: /Download original/ })).toBeTruthy();
  expect(location.hash).toBe("");
  expect(sessionStorage.length).toBe(0);

  await fireEvent.click(screen.getByRole("button", { name: "Load more" }));
  await waitFor(() => expect(screen.getByText(/Second page/)).toBeTruthy());

  const viewerCalls = fetchMock.mock.calls.filter(([path]) => String(path).includes("/viewer?"));
  expect(viewerCalls).toHaveLength(2);
  expect(String(viewerCalls[1]?.[0])).toContain(`build_id=${buildID}`);
  for (const [, request] of viewerCalls) {
    expect(new Headers(request?.headers).get("X-Docbank-Web-Session")).toBe("bounded");
  }
});

it("rejects malformed document links before session bootstrap", async () => {
  history.replaceState(null, "", "/documents/42/versions/not-a-version");
  const fetchMock = vi.spyOn(globalThis, "fetch");
  render(DocumentViewer);
  expect(await screen.findByText("This document link is malformed.")).toBeTruthy();
  expect(fetchMock).not.toHaveBeenCalled();
});

it("stays locked when an in-flight document request completes", async () => {
  history.replaceState(null, "", `/documents/42/versions/${versionID}`);
  const pendingViewer = deferred<Response>();
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, request) => {
    const path = String(input);
    if (path === "/api/daemon/web-session" && request?.method === "POST") {
      return json({ token: "bounded", upload_secret: "unused" }, 201);
    }
    if (path.includes("/viewer?")) return pendingViewer.promise;
    if (path === "/api/daemon/web-session" && request?.method === "DELETE") {
      return new Response(null, { status: 204 });
    }
    throw new Error(`unexpected request ${path}`);
  });

  render(DocumentViewer);
  const lock = await screen.findByRole("button", { name: "Lock document session" });
  await fireEvent.click(lock);
  expect(await screen.findByText("This document session is locked.")).toBeTruthy();
  expect(screen.queryByText("Opening document…")).toBeNull();

  pendingViewer.resolve(json(viewer(0)));
  await Promise.resolve();
  expect(screen.queryByRole("heading", { name: "registration.pdf" })).toBeNull();
});

it("revokes a bootstrapped session that arrives after cleanup", async () => {
  history.replaceState(null, "", `/documents/42/versions/${versionID}`);
  const pendingBootstrap = deferred<Response>();
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, request) => {
    const path = String(input);
    if (path === "/api/daemon/web-session" && request?.method === "POST") {
      return pendingBootstrap.promise;
    }
    if (path === "/api/daemon/web-session" && request?.method === "DELETE") {
      return new Response(null, { status: 204 });
    }
    throw new Error(`unexpected request ${path}`);
  });

  const rendered = render(DocumentViewer);
  rendered.unmount();
  pendingBootstrap.resolve(json({ token: "late-token", upload_secret: "unused" }, 201));

  await waitFor(() => {
    const revoke = fetchMock.mock.calls.find(
      ([path, request]) =>
        String(path) === "/api/daemon/web-session" && request?.method === "DELETE",
    );
    expect(revoke).toBeTruthy();
    expect(new Headers(revoke?.[1]?.headers).get("X-Docbank-Web-Session")).toBe(
      "late-token",
    );
  });
});
