<script lang="ts">
  import { onMount } from "svelte";
  import FileTextIcon from "@lucide/svelte/icons/file-text";
  import HistoryIcon from "@lucide/svelte/icons/history";
  import LogOutIcon from "@lucide/svelte/icons/log-out";
  import MapPinIcon from "@lucide/svelte/icons/map-pin";
  import ShieldCheckIcon from "@lucide/svelte/icons/shield-check";
  import {
    Button,
    Card,
    Chip,
    CopyButton,
    EmptyState,
    IconButton,
    Spinner,
    ThemeToggle,
    TopBar,
  } from "@kenn-io/kit-ui";
  import DownloadButton from "./DownloadButton.svelte";
  import ProvenanceDrawer from "./ProvenanceDrawer.svelte";
  import {
    APIError,
    bootstrapBrowserSession,
    documentViewer,
    parseDocumentViewerPath,
    revokeSession,
    takeFragmentSession,
    type DocumentViewer as DocumentViewerRecord,
  } from "./api.js";
  import { formatBytes, formatDate } from "./format.js";

  const target = parseDocumentViewerPath();
  let session = $state("");
  let record = $state<DocumentViewerRecord | null>(null);
  let loading = $state(Boolean(target));
  let loadingMore = $state(false);
  let error = $state(target ? "" : "This document link is malformed.");
  let provenanceOpen = $state(false);
  let generation = 0;

  const isCurrent = $derived(
    Boolean(record && record.node.current_version_id === record.version.id),
  );
  const transcript = $derived(
    record?.rendition?.segments.map((segment) => segment.text).join("\n\n") ?? "",
  );

  onMount(() => {
    if (!target) return;
    const fragment = takeFragmentSession();
    void open(fragment?.token ?? "");
    return () => {
      generation += 1;
    };
  });

  async function open(fragmentToken: string): Promise<void> {
    const request = ++generation;
    let token = fragmentToken;
    loading = true;
    error = "";
    try {
      token ||= (await bootstrapBrowserSession()).token;
      if (request !== generation) return;
      session = token;
      const next = await documentViewer(token, target!);
      if (request !== generation) return;
      record = next;
    } catch (cause) {
      if (request !== generation) return;
      if (token) {
        void revokeSession(token).catch(() => undefined);
      }
      session = "";
      record = null;
      error = accessError(cause);
    } finally {
      if (request === generation) loading = false;
    }
  }

  function accessError(cause: unknown): string {
    if (cause instanceof APIError && (cause.status === 401 || cause.status === 403)) {
      return "You do not have access to this document link.";
    }
    if (cause instanceof APIError && cause.status === 404) {
      return "This exact document version was not found.";
    }
    return cause instanceof Error ? cause.message : String(cause);
  }

  async function loadMore(): Promise<void> {
    if (!target || !record?.rendition || loadingMore) return;
    const request = generation;
    loadingMore = true;
    error = "";
    const expected = record.rendition;
    try {
      const next = await documentViewer(
        session,
        target,
        expected.segments.length,
        expected.build_id,
      );
      if (request !== generation) return;
      if (
        next.version.id !== record.version.id ||
        next.rendition?.build_id !== expected.build_id ||
        next.rendition.source_sha256 !== record.version.blob_hash
      ) {
        throw new Error("The OCR authority changed while loading this document");
      }
      record = {
        ...record,
        rendition: {
          ...next.rendition,
          segments: [...expected.segments, ...next.rendition.segments],
        },
      };
    } catch (cause) {
      if (request !== generation) return;
      if (cause instanceof APIError && cause.status === 401) session = "";
      error = accessError(cause);
    } finally {
      loadingMore = false;
    }
  }

  async function lock(): Promise<void> {
    generation += 1;
    const token = session;
    session = "";
    record = null;
    provenanceOpen = false;
    error = "This document session is locked.";
    try {
      await revokeSession(token);
    } catch {
      // The in-memory credential is cleared even if the daemon is unreachable.
    }
  }
</script>

<div class="viewer-shell">
  <TopBar>
    {#snippet left()}
      <div class="brand">
        <span class="brand-mark">D</span>
        <div><strong>Docbank</strong><span>Document</span></div>
      </div>
    {/snippet}
    {#snippet right()}
      <ThemeToggle size="sm" />
      {#if session}
        <IconButton size="sm" ariaLabel="Lock document session" onclick={() => void lock()}>
          <LogOutIcon size="14" aria-hidden="true" />
        </IconButton>
      {/if}
    {/snippet}
  </TopBar>

  <main>
    {#if loading}
      <Card level="raised" title="Opening document…" eyebrow="DOCBANK">
        <p class="loading"><Spinner size={16} /> Checking access…</p>
      </Card>
    {:else if error && !record}
      <EmptyState title="Document unavailable" description={error}>
        {#snippet icon()}<FileTextIcon size="24" />{/snippet}
      </EmptyState>
    {:else if record}
      <section class="document-heading">
        <div>
          <span class="eyebrow">SAVED VERSION</span>
          <h1>{record.node.name}</h1>
          <p>{record.node.path || `Trashed node · id:${record.node.id}`}</p>
        </div>
        <div class="actions">
          <Button size="sm" surface="soft" onclick={() => (provenanceOpen = true)}>
            <MapPinIcon size="14" aria-hidden="true" /> Provenance
          </Button>
          <DownloadButton
            {session}
            node={record.node}
            version={record.version}
            label="Download original"
            onauthfailure={(cause) => {
              session = "";
              error = accessError(cause);
            }}
          />
        </div>
      </section>

      {#if error}<p class="error" role="alert">{error}</p>{/if}

      <div class="metadata-grid">
        <Card level="raised" title="Document details" eyebrow="SAVED VERSION">
          <dl>
            <div><dt>Status</dt><dd><Chip size="xs" tone={isCurrent ? "success" : "muted"}>{isCurrent ? "Current" : "Historical"}</Chip></dd></div>
            <div class="identity"><dt>Version</dt><dd><code>{record.version.id}</code><CopyButton text={record.version.id} ariaLabel="Copy version ID" /></dd></div>
            <div><dt>Node</dt><dd>id:{record.node.id} · revision {record.version.node_revision}</dd></div>
            <div><dt>Recorded</dt><dd>{formatDate(record.version.recorded_at)}</dd></div>
            <div><dt>Original</dt><dd>{record.version.mime_type || "Unknown type"} · {formatBytes(record.version.size)}</dd></div>
            <div class="identity"><dt>SHA-256</dt><dd><code>{record.version.blob_hash}</code><CopyButton text={record.version.blob_hash} ariaLabel="Copy source hash" /></dd></div>
          </dl>
        </Card>

        <Card level="raised" title="Processing details" eyebrow="EXTRACTED TEXT">
          {#if record.rendition}
            <dl>
              <div><dt>Evidence</dt><dd><Chip size="xs" tone={record.rendition.completeness === "complete" ? "success" : "warning"}>{record.rendition.completeness}</Chip></dd></div>
              <div><dt>Published</dt><dd>{formatDate(record.rendition.published_at)}</dd></div>
              <div class="identity"><dt>Build</dt><dd><code>{record.rendition.build_id}</code><CopyButton text={record.rendition.build_id} ariaLabel="Copy OCR build ID" /></dd></div>
              <div class="identity"><dt>Evidence checksum</dt><dd><code>{record.rendition.evidence_checksum}</code><CopyButton text={record.rendition.evidence_checksum} ariaLabel="Copy evidence checksum" /></dd></div>
            </dl>
          {:else}
            <p class="muted">No active OCR or transcript rendition is recorded for this exact version.</p>
          {/if}
        </Card>
      </div>

      <div class="transcript-card">
        <Card level="raised" padding="none" ariaLabel="OCR transcript">
          <header>
            <div><span class="eyebrow">EXTRACTED TEXT</span><h2>OCR transcript</h2></div>
            {#if record.rendition}<span>{record.rendition.segments.length} of {record.rendition.total} segments</span>{/if}
          </header>
          {#if transcript}
            <pre>{transcript}</pre>
            {#if record.rendition && record.rendition.segments.length < record.rendition.total}
              <footer><Button size="sm" surface="soft" disabled={loadingMore} onclick={() => void loadMore()}>{#if loadingMore}<Spinner size={13} />{/if}Load more</Button></footer>
            {/if}
          {:else}
            <EmptyState title="No transcript available" description="The original remains available for verified download.">
              {#snippet icon()}<HistoryIcon size="22" />{/snippet}
            </EmptyState>
          {/if}
        </Card>
      </div>

      <p class="integrity-note"><ShieldCheckIcon size="14" aria-hidden="true" /> This link always opens this saved version and its extracted text.</p>
    {/if}
  </main>
</div>

{#if provenanceOpen && record}
  <ProvenanceDrawer
    {session}
    node={record.node}
    path={record.node.path ?? ""}
    onclose={() => (provenanceOpen = false)}
    onauthfailure={(cause) => {
      session = "";
      error = accessError(cause);
    }}
  />
{/if}

<style>
  .viewer-shell { min-height: 100vh; background: var(--bg-base); color: var(--text); }
  main { width: min(1180px, calc(100% - 32px)); margin: 0 auto; padding: var(--space-6) 0 var(--space-8); }
  .brand, .brand > div, .document-heading, .actions, .loading, .integrity-note { display: flex; align-items: center; }
  .brand { gap: var(--space-2); }
  .brand > div { align-items: flex-start; flex-direction: column; line-height: 1.1; }
  .brand > div span { color: var(--text-muted); font-size: var(--font-size-xs); }
  .brand-mark { display: grid; width: 30px; height: 30px; place-items: center; border-radius: var(--radius-sm); background: var(--accent); color: var(--accent-contrast); font-weight: 800; }
  .document-heading { justify-content: space-between; gap: var(--space-5); margin-bottom: var(--space-5); }
  .document-heading h1, .transcript-card h2 { margin: 3px 0 0; }
  .document-heading p { margin: 5px 0 0; color: var(--text-muted); }
  .actions { flex-wrap: wrap; justify-content: flex-end; gap: var(--space-2); }
  .eyebrow { color: var(--accent); font-size: var(--font-size-xs); font-weight: 750; letter-spacing: .09em; }
  .metadata-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--space-4); margin-bottom: var(--space-4); }
  dl { display: grid; gap: var(--space-3); margin: 0; }
  dl > div { display: grid; grid-template-columns: 110px minmax(0, 1fr); gap: var(--space-3); align-items: start; }
  dt { color: var(--text-muted); font-size: var(--font-size-xs); text-transform: uppercase; letter-spacing: .05em; }
  dd { min-width: 0; margin: 0; }
  .identity dd { display: flex; align-items: center; gap: var(--space-1); }
  code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .transcript-card header { display: flex; justify-content: space-between; align-items: flex-end; gap: var(--space-3); padding: var(--space-4) var(--space-5); border-bottom: 1px solid var(--border); }
  .transcript-card header > span, .muted { color: var(--text-muted); font-size: var(--font-size-sm); }
  pre { margin: 0; padding: var(--space-5); white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; line-height: 1.7; }
  footer { padding: 0 var(--space-5) var(--space-5); }
  .integrity-note, .loading { gap: var(--space-2); color: var(--text-muted); font-size: var(--font-size-sm); }
  .integrity-note { justify-content: center; margin-top: var(--space-4); }
  .error { padding: var(--space-3); border: 1px solid var(--danger); border-radius: var(--radius-sm); color: var(--danger); }
  @media (max-width: 760px) {
    main { width: min(100% - 20px, 1180px); padding-top: var(--space-4); }
    .document-heading { align-items: flex-start; flex-direction: column; }
    .actions { justify-content: flex-start; }
    .metadata-grid { grid-template-columns: 1fr; }
    dl > div { grid-template-columns: 90px minmax(0, 1fr); }
    .transcript-card header { align-items: flex-start; flex-direction: column; }
  }
</style>
