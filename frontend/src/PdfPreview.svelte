<script lang="ts">
  import { onMount } from "svelte";
  import { Button, Spinner } from "@kenn-io/kit-ui";
  import type { PDFDocumentLoadingTask, PDFDocumentProxy, RenderTask } from "pdfjs-dist";
  import workerURL from "pdfjs-dist/build/pdf.worker.min.mjs?url";

  let { blob }: { blob: Blob } = $props();
  let canvas: HTMLCanvasElement;
  let pageNumber = $state(1);
  let pageCount = $state(0);
  let loading = $state(true);
  let error = $state("");
  let disposed = false;
  let rendering = false;
  let pdf: PDFDocumentProxy | undefined;
  let loadingTask: PDFDocumentLoadingTask | undefined;
  let renderTask: RenderTask | undefined;

  onMount(() => {
    void open();
    return () => {
      disposed = true;
      renderTask?.cancel();
      void loadingTask?.destroy().catch(() => undefined);
      canvas.width = 0;
      canvas.height = 0;
    };
  });

  async function open(): Promise<void> {
    try {
      const [library, bytes] = await Promise.all([import("pdfjs-dist"), blob.arrayBuffer()]);
      if (disposed) return;
      library.GlobalWorkerOptions.workerSrc = workerURL;
      const assetBase = `/assets/pdfjs/${library.version}/`;
      loadingTask = library.getDocument({
        data: new Uint8Array(bytes),
        cMapUrl: `${assetBase}cmaps/`,
        standardFontDataUrl: `${assetBase}standard_fonts/`,
        wasmUrl: `${assetBase}wasm/`,
      });
      pdf = await loadingTask.promise;
      if (disposed) return;
      pageCount = pdf.numPages;
      await draw(1);
    } catch (cause) {
      fail(cause);
    }
  }

  function fail(cause: unknown): void {
    if (disposed) return;
    loading = false;
    error = cause instanceof Error ? cause.message : String(cause);
    const failedTask = loadingTask;
    loadingTask = undefined;
    pdf = undefined;
    renderTask = undefined;
    void failedTask?.destroy().catch(() => undefined);
  }

  async function draw(number: number): Promise<void> {
    if (!pdf || disposed || rendering) return;
    rendering = true;
    loading = true;
    error = "";
    try {
      const page = await pdf.getPage(number);
      if (disposed) return;
      const natural = page.getViewport({ scale: 1 });
      // One page and at most 1600px on its longest side bounds canvas memory.
      const scale = Math.min(2, 1600 / Math.max(natural.width, natural.height));
      const viewport = page.getViewport({ scale });
      canvas.width = Math.ceil(viewport.width);
      canvas.height = Math.ceil(viewport.height);
      renderTask = page.render({ canvas, viewport });
      await renderTask.promise;
      if (disposed) return;
      pageNumber = number;
      page.cleanup();
      loading = false;
    } catch (cause) {
      fail(cause);
    } finally {
      rendering = false;
    }
  }
</script>

<div class="pdf-preview">
  <div class="pdf-controls" aria-label="PDF navigation">
    <Button size="sm" surface="soft" disabled={loading || Boolean(error) || pageNumber <= 1} onclick={() => void draw(pageNumber - 1)}>Previous</Button>
    <span aria-live="polite">{pageCount ? `Page ${pageNumber} of ${pageCount}` : "Opening PDF"}</span>
    <Button size="sm" surface="soft" disabled={loading || Boolean(error) || pageNumber >= pageCount} onclick={() => void draw(pageNumber + 1)}>Next</Button>
  </div>
  {#if loading}<p class="pdf-status" role="status"><Spinner size={16} /> Rendering PDF…</p>{/if}
  {#if error}<p class="pdf-status" role="alert">PDF preview unavailable: {error} Use Download original to open the file.</p>{/if}
  <div class="pdf-stage" aria-busy={loading}>
    <div role="img" aria-label={`PDF page ${pageNumber}`} hidden={loading || Boolean(error)}>
      <canvas bind:this={canvas} aria-hidden="true"></canvas>
    </div>
  </div>
</div>

<style>
  .pdf-controls, .pdf-status {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    padding: var(--space-3);
    color: var(--text-muted);
  }
  .pdf-controls span { font-size: var(--font-size-sm); }
  .pdf-stage {
    max-height: 72vh;
    overflow: auto;
    padding: var(--space-3);
    background: var(--bg-inset);
  }
  canvas {
    display: block;
    width: min(100%, 900px);
    height: auto;
    margin: auto;
    background: white;
  }
</style>
