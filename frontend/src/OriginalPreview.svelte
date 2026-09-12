<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import FileTextIcon from "@lucide/svelte/icons/file-text";
  import { EmptyState, Spinner } from "@kenn-io/kit-ui";
  import { APIError, type ContentVersion, type Node } from "./api.js";
  import { formatBytes } from "./format.js";
  import {
    loadOriginalPreview,
    MAX_INLINE_PREVIEW_BYTES,
    originalPreviewKind,
  } from "./originalPreview.js";

  let {
    session,
    node,
    version,
    onauthfailure,
  }: {
    session: string;
    node: Node;
    version: ContentVersion;
    onauthfailure: (cause: unknown) => void;
  } = $props();

  const supportedKind = $derived(originalPreviewKind(version.mime_type, version.size));
  let objectURL = $state("");
  let progress = $state(0);
  let error = $state("");
  let generation = 0;
  let controller: AbortController | null = null;

  onMount(() => {
    if (supportedKind) void openPreview();
  });

  onDestroy(() => {
    generation += 1;
    controller?.abort();
    if (objectURL) URL.revokeObjectURL(objectURL);
  });

  async function openPreview(): Promise<void> {
    const request = ++generation;
    const active = new AbortController();
    controller = active;
    error = "";
    try {
      const loaded = await loadOriginalPreview(
        session,
        node,
        version,
        active.signal,
        (next) => {
          if (request === generation) progress = next.received;
        },
      );
      if (request !== generation) {
        URL.revokeObjectURL(loaded);
        return;
      }
      objectURL = loaded;
    } catch (cause) {
      if (request !== generation || (cause instanceof DOMException && cause.name === "AbortError")) {
        return;
      }
      if (cause instanceof APIError && cause.status === 401) {
        onauthfailure(cause);
        return;
      }
      error = cause instanceof Error ? cause.message : String(cause);
    } finally {
      if (request === generation) controller = null;
    }
  }
</script>

{#if !supportedKind}
  <EmptyState
    title={version.size > MAX_INLINE_PREVIEW_BYTES
      ? `Preview unavailable for files larger than ${formatBytes(MAX_INLINE_PREVIEW_BYTES)}.`
      : "Preview unavailable for this file type."}
    description="Download the verified original to open it in another application."
  >
    {#snippet icon()}<FileTextIcon size="22" />{/snippet}
  </EmptyState>
{:else if error}
  <EmptyState title="Original preview unavailable" description={error}>
    {#snippet icon()}<FileTextIcon size="22" />{/snippet}
  </EmptyState>
{:else if !objectURL}
  <p class="preview-loading" aria-live="polite">
    <Spinner size={16} /> Verifying original…
    {#if progress > 0}<span>{formatBytes(progress)} of {formatBytes(version.size)}</span>{/if}
  </p>
{:else if supportedKind === "image"}
  <div class="image-stage">
    <img src={objectURL} alt={`Original ${node.name}`} />
  </div>
{:else}
  <iframe src={`${objectURL}#view=Fit&navpanes=0`} title={`Original ${node.name}`}></iframe>
{/if}

<style>
  .preview-loading {
    display: flex;
    min-height: 340px;
    margin: 0;
    align-items: center;
    justify-content: center;
    gap: var(--space-2);
    color: var(--text-muted);
  }

  .preview-loading span {
    font-size: var(--font-size-xs);
  }

  .image-stage {
    display: grid;
    min-height: 340px;
    max-height: 72vh;
    overflow: auto;
    place-items: center;
    padding: var(--space-4);
    background: var(--bg-inset);
  }

  img {
    display: block;
    max-width: 100%;
    max-height: 68vh;
    object-fit: contain;
  }

  iframe {
    display: block;
    width: 100%;
    height: min(72vh, 820px);
    min-height: 520px;
    border: 0;
    background: white;
  }

  @media (max-width: 760px) {
    iframe { min-height: 440px; }
    .preview-loading, .image-stage { min-height: 280px; }
  }
</style>
