import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import PdfPreview from "./PdfPreview.svelte";

const library = vi.hoisted(() => ({
  getDocument: vi.fn(),
  GlobalWorkerOptions: { workerSrc: "" },
  version: "6.3.289",
}));
vi.mock("pdfjs-dist", () => library);

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(next => { resolve = next; });
  return { promise, resolve };
}

const blob = { arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer } as Blob;
const paint = vi.fn();
const cancel = vi.fn();
const destroy = vi.fn();
const getPage = vi.fn();
const page = {
  getViewport: ({ scale }: { scale: number }) => ({ width: 1200 * scale, height: 2400 * scale }),
  render: paint,
  cleanup: vi.fn(),
};

beforeEach(() => {
  paint.mockReturnValue({ promise: Promise.resolve(), cancel });
  destroy.mockResolvedValue(undefined);
  getPage.mockResolvedValue(page);
  library.getDocument.mockReturnValue({
    promise: Promise.resolve({ numPages: 2, getPage }),
    destroy,
  });
});

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

it("renders verified bytes with bounded canvases and releases the PDF on close", async () => {
  const result = render(PdfPreview, { blob });
  await screen.findByRole("img", { name: "PDF page 1" });
  expect(library.getDocument).toHaveBeenCalledWith(expect.objectContaining({
    data: new Uint8Array([1, 2, 3]),
    wasmUrl: "/assets/pdfjs/6.3.289/wasm/",
  }));
  const canvas = result.container.querySelector("canvas")!;
  expect(canvas.width).toBe(800);
  expect(canvas.height).toBe(1600);
  await fireEvent.click(screen.getByRole("button", { name: "Next" }));
  await screen.findByRole("img", { name: "PDF page 2" });
  expect(screen.getByText("Page 2 of 2")).toBeTruthy();
  expect(getPage.mock.calls.map(([number]) => number)).toEqual([1, 2]);
  result.unmount();
  expect(cancel).toHaveBeenCalled();
  expect(destroy).toHaveBeenCalledOnce();
  expect(canvas.width).toBe(0);
  expect(canvas.height).toBe(0);
});

it("does not paint a page that finishes loading after the document is closed", async () => {
  const pending = deferred<typeof page>();
  getPage.mockReturnValueOnce(pending.promise);
  const result = render(PdfPreview, { blob });
  await waitFor(() => expect(getPage).toHaveBeenCalledOnce());
  result.unmount();
  pending.resolve(page);
  await pending.promise;
  expect(paint).not.toHaveBeenCalled();
  expect(destroy).toHaveBeenCalledOnce();
});

it("shows a download fallback when the PDF cannot be decoded", async () => {
  library.getDocument.mockReturnValue({
    promise: Promise.reject(new Error("Invalid PDF")),
    destroy,
  });
  render(PdfPreview, { blob });
  expect((await screen.findByRole("alert")).textContent).toContain("Use Download original");
  expect(screen.queryByRole("img")).toBeNull();
  expect(destroy).toHaveBeenCalledOnce();
});

it("does not start a PDF worker after a late blob read on a closed document", async () => {
  const bytes = deferred<ArrayBuffer>();
  const result = render(PdfPreview, { blob: { arrayBuffer: () => bytes.promise } as Blob });
  result.unmount();
  bytes.resolve(new ArrayBuffer(0));
  await bytes.promise;
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(library.getDocument).not.toHaveBeenCalled();
});
