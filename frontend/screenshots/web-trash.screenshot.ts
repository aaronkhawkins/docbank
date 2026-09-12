import { expect, test } from "@playwright/test";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

const execFileAsync = promisify(execFile);
const here = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(here, "..", "..");
const binary = path.join(repositoryRoot, "docbank");
const screenshotAPIKey = "synthetic-screenshot-api-key";
const screenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-trash-confirmation.png",
);
const restoreScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-trash-restore-confirmation.png",
);
const tagAssignmentScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-tag-assignment.png",
);
const tagCatalogScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-tag-catalog.png",
);
const auditEvidenceScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-audit-evidence.png",
);
const storageScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-multi-store-storage.png",
);
const tuiStorageScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "tui-multi-store-storage.png",
);
const vaultBrowserScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-vault-browser.png",
);
const searchResultsScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-search-results.png",
);
const retainedVersionScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-retained-version-download.png",
);
const packedStorageScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-storage-status.png",
);
const documentViewerScreenshotPath = path.join(
  repositoryRoot,
  ".superpowers",
  "screenshots",
  "web-document-viewer.png",
);

test.describe("Docbank web screenshots", () => {
  let workspace = "";
  let vault = "";
  let webURL = "";
  let viewerNodeID = 0;
  let viewerVersionID = "";

  async function runDocbank(args: string[]): Promise<string> {
    const result = await execFileAsync(binary, args, {
      cwd: repositoryRoot,
      env: {
        ...process.env,
        DOCBANK_HOME: vault,
      },
      maxBuffer: 1024 * 1024,
      timeout: 60_000,
    });
    return result.stdout.trim();
  }

  async function placeForScreenshot(selector: string, move = false): Promise<void> {
    const previewArgs = [
      "storage",
      "place",
      selector,
      "--to",
      "archive",
      "--json",
    ];
    if (move) previewArgs.push("--move");
    const preview = JSON.parse(await runDocbank(previewArgs)) as {
      preview_token?: unknown;
    };
    if (typeof preview.preview_token !== "string" || preview.preview_token === "") {
      throw new Error("storage placement preview omitted its token");
    }
    const operation = JSON.parse(
      await runDocbank([
        "storage",
        "place",
        "--run",
        "--token",
        preview.preview_token,
        "--json",
      ]),
    ) as { id?: unknown };
    if (typeof operation.id !== "string" || operation.id === "") {
      throw new Error("storage placement start omitted its operation ID");
    }
    for (let attempt = 0; attempt < 100; attempt += 1) {
      const current = JSON.parse(
        await runDocbank(["jobs", "show", operation.id, "--json"]),
      ) as { state?: unknown; error?: unknown };
      if (current.state === "completed") return;
      if (current.state === "failed" || current.state === "cancelled") {
        throw new Error(
          `storage placement ${current.state}: ${String(current.error ?? "")}`,
        );
      }
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    throw new Error("storage placement did not complete");
  }

  test.beforeAll(async () => {
    workspace = await mkdtemp(path.join(tmpdir(), "docbank-screenshot-"));
    vault = path.join(workspace, "vault");
    await mkdir(path.dirname(screenshotPath), { recursive: true, mode: 0o700 });
    await rm(screenshotPath, { force: true });
    await rm(restoreScreenshotPath, { force: true });
    await rm(tagAssignmentScreenshotPath, { force: true });
    await rm(tagCatalogScreenshotPath, { force: true });
    await rm(auditEvidenceScreenshotPath, { force: true });
    await rm(storageScreenshotPath, { force: true });
    await rm(tuiStorageScreenshotPath, { force: true });
    await rm(vaultBrowserScreenshotPath, { force: true });
    await rm(searchResultsScreenshotPath, { force: true });
    await rm(retainedVersionScreenshotPath, { force: true });
    await rm(packedStorageScreenshotPath, { force: true });
    await rm(documentViewerScreenshotPath, { force: true });
    const archive = path.join(workspace, "archive-store");
    await mkdir(vault, { recursive: true, mode: 0o700 });
    await mkdir(archive, { recursive: true, mode: 0o700 });
    await writeFile(
      path.join(vault, "config.toml"),
      `[server]\napi_key = ${JSON.stringify(screenshotAPIKey)}\n\n[store_bindings.archive]\nkind = "filesystem"\npath = ${JSON.stringify(archive)}\npriority = 20\n`,
      { mode: 0o600 },
    );
    const reports = path.join(workspace, "synthetic", "Reports");
    await mkdir(reports, { recursive: true, mode: 0o700 });
    await writeFile(
      path.join(reports, "quarterly-tax-report.txt"),
      "Synthetic quarterly tax report for screenshot validation.\n",
      { mode: 0o600 },
    );
    await writeFile(
      path.join(reports, "filing-checklist.md"),
      "# Filing checklist\n\n- Review totals\n- Confirm signatures\n",
      { mode: 0o600 },
    );
    await writeFile(
      path.join(reports, "supporting-schedule.csv"),
      "category,amount\nSynthetic revenue,125000\nSynthetic expense,42000\n",
      { mode: 0o600 },
    );
    const archiveReference = path.join(
      workspace,
      "synthetic",
      "archive-reference.txt",
    );
    await writeFile(
      archiveReference,
      "Synthetic remote-only reference for storage status.\n",
      { mode: 0o600 },
    );

    await runDocbank(["add", reports, "--dest", "/", "--progress", "plain"]);
    await runDocbank([
      "add",
      archiveReference,
      "--dest",
      "/",
      "--progress",
      "plain",
    ]);
    await runDocbank(["tag", "create", "tax"]);
    await runDocbank(["tag", "create", "reviewed"]);
    await runDocbank([
      "tag",
      "assign",
      "tax",
      "/Reports/quarterly-tax-report.txt",
    ]);
    const storePreview = JSON.parse(
      await runDocbank([
        "storage",
        "add",
        "archive",
        "--binding",
        "archive",
        "--json",
      ]),
    ) as { preview_token?: unknown };
    if (
      typeof storePreview.preview_token !== "string" ||
      storePreview.preview_token === ""
    ) {
      throw new Error("storage registration preview omitted its token");
    }
    await runDocbank([
      "storage",
      "add",
      "--run",
      "--token",
      storePreview.preview_token,
      "--json",
    ]);
    await placeForScreenshot("/Reports");
    await placeForScreenshot("/archive-reference.txt", true);
    const preview = JSON.parse(
      await runDocbank(["audit", "enable", "/Reports", "--json"]),
    ) as { preview_token?: unknown };
    if (
      typeof preview.preview_token !== "string" ||
      preview.preview_token === ""
    ) {
      throw new Error("audit enrollment preview omitted its token");
    }
    await runDocbank([
      "audit",
      "enable",
      "--run",
      "--token",
      preview.preview_token,
      "--acknowledge-permanent-retention",
      "--json",
    ]);
    let extractionReady = false;
    for (let attempt = 0; attempt < 100; attempt += 1) {
      const report = JSON.parse(
        await runDocbank(["search", "Synthetic", "--json"]),
      ) as { hits?: unknown[] };
      if ((report.hits?.length ?? 0) > 0) {
        extractionReady = true;
        break;
      }
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    if (!extractionReady) {
      throw new Error("synthetic text extraction did not complete");
    }
    const revisedReport = path.join(
      workspace,
      "synthetic",
      "revised-quarterly-tax-report.txt",
    );
    await writeFile(
      revisedReport,
      "Synthetic quarterly tax report with reviewed totals.\n",
      { mode: 0o600 },
    );
    await runDocbank([
      "put",
      revisedReport,
      "/Reports/quarterly-tax-report.txt",
    ]);
    const viewerNode = JSON.parse(
      await runDocbank(["stat", "/Reports/quarterly-tax-report.txt", "--json"]),
    ) as { id?: unknown };
    const viewerVersions = JSON.parse(
      await runDocbank([
        "versions",
        "list",
        "/Reports/quarterly-tax-report.txt",
        "--json",
      ]),
    ) as { items?: Array<{ id?: unknown; blob_hash?: unknown }> };
    if (
      typeof viewerNode.id !== "number" ||
      viewerNode.id < 1 ||
      typeof viewerVersions.items?.[1]?.id !== "string" ||
      typeof viewerVersions.items?.[1]?.blob_hash !== "string"
    ) {
      throw new Error("synthetic historical document authority is incomplete");
    }
    viewerNodeID = viewerNode.id;
    viewerVersionID = viewerVersions.items[1].id;
    webURL = await runDocbank(["web", "--no-browser"]);
    const browserURL = new URL(webURL);
    const port = Number(browserURL.port);
    if (
      browserURL.protocol !== "http:" ||
      !/^docbank-[0-9a-f]{32}\.localhost$/.test(browserURL.hostname) ||
      !Number.isInteger(port) ||
      port < 1 ||
      port > 65_535 ||
      browserURL.username !== "" ||
      browserURL.password !== "" ||
      browserURL.pathname !== "/" ||
      browserURL.search !== "" ||
      browserURL.hash === ""
    ) {
      throw new Error("docbank web returned an unexpected browser URL");
    }

    const transcript = "Synthetic quarterly tax report\nFiling year: 2026";
    const structured = JSON.stringify({
      schema_version: 1,
      markdown: transcript,
      layout: [{ label: "text", boxes: [[10, 20, 300, 80]] }],
    });
    const sha256 = (value: string) =>
      createHash("sha256").update(value).digest("hex");
    const metadata = {
      content_version_id: viewerVersionID,
      source_sha256: viewerVersions.items[1].blob_hash,
      submission_key: sha256("synthetic-screenshot-submission"),
      family: "pdf",
      engine: "focr",
      engine_version: "0.8.0",
      model: "unlimited-ocr.v0.7.0.int8.focrq",
      recipe: "unlimited-ocr-ffn-int8-attn-bf16-lmhead-bf16-v1",
      manifest_sha256: sha256("synthetic-screenshot-manifest"),
      manifest_bytes: 4_157_448_783,
      produced_at: "2026-09-07T12:34:56.000000000Z",
      transcript_sha256: sha256(transcript),
      transcript_bytes: Buffer.byteLength(transcript),
      structured_sha256: sha256(structured),
      structured_bytes: Buffer.byteLength(structured),
    };
    const form = new FormData();
    form.append("metadata", new Blob([JSON.stringify(metadata)], { type: "application/json" }), "metadata.json");
    form.append("transcript", new Blob([transcript], { type: "text/markdown; charset=utf-8" }), "transcript.md");
    form.append("structured", new Blob([structured], { type: "application/json" }), "structured.json");
    const suppliedOCRURL = new URL(`/api/v1/nodes/${viewerNodeID}/supplied-ocr`, webURL);
    const publication = await fetch(suppliedOCRURL, {
      method: "POST",
      headers: { "X-Api-Key": screenshotAPIKey },
      body: form,
    });
    if (!publication.ok) {
      throw new Error(`synthetic OCR publication failed: ${publication.status} ${await publication.text()}`);
    }
  });

  test.afterAll(async () => {
    if (vault) {
      let stopped = false;
      let stopError: unknown;
      for (let attempt = 0; attempt < 2; attempt += 1) {
        try {
          await runDocbank(["daemon", "stop"]);
          const status = JSON.parse(
            await runDocbank(["daemon", "status", "--json"]),
          ) as { running?: unknown };
          if (status.running !== false) {
            throw new Error("synthetic Docbank daemon is still running");
          }
          stopped = true;
          break;
        } catch (cause) {
          stopError = cause;
        }
      }
      if (!stopped) {
        throw new Error(
          `could not stop the synthetic Docbank daemon; workspace retained at ${workspace}`,
          { cause: stopError },
        );
      }
    }
    if (workspace) await rm(workspace, { recursive: true, force: true });
  });

  test("saved document viewer", async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem("docbank-theme", "dark");
    });
    const viewerURL = new URL(webURL);
    viewerURL.pathname = `/documents/${viewerNodeID}/versions/${viewerVersionID}`;
    await page.goto(viewerURL.toString(), { waitUntil: "domcontentloaded" });
    await page.addStyleTag({
      content: `
        *, *::before, *::after {
          animation-duration: 0.001s !important;
          animation-delay: 0s !important;
          transition-duration: 0s !important;
          caret-color: transparent !important;
        }
      `,
    });
    await expect(
      page.getByRole("heading", { name: "quarterly-tax-report.txt" }),
    ).toBeVisible();
    await expect(page.getByText("Historical", { exact: true })).toBeVisible();
    await expect(page.getByText("Synthetic quarterly tax report", { exact: false })).toBeVisible();
    await expect(page.getByRole("button", { name: /Download original/ })).toBeVisible();
    await page.screenshot({
      path: documentViewerScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });
  });

  test("trash confirmation", async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem("docbank-theme", "dark");
    });
    await page.goto(webURL, { waitUntil: "domcontentloaded" });
    await page.addStyleTag({
      content: `
        *, *::before, *::after {
          animation-duration: 0.001s !important;
          animation-delay: 0s !important;
          transition-duration: 0s !important;
          caret-color: transparent !important;
        }
      `,
    });

    await page.getByRole("cell", { name: "Reports", exact: true }).dblclick();
    const report = page.getByRole("cell", {
      name: "quarterly-tax-report.txt",
      exact: true,
    });
    await expect(report).toBeVisible();
    await report.click();
    await expect(page.getByText("tax", { exact: true })).toBeVisible();
    await expect(page.getByText("Protected", { exact: true })).toBeVisible();
    await page.screenshot({
      path: vaultBrowserScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });

    await page.getByRole("button", { name: "Version history" }).click();
    const versions = page.getByRole("dialog", {
      name: "Immutable version history for /Reports/quarterly-tax-report.txt",
    });
    await expect(versions).toContainText("2 retained versions");
    await versions.getByRole("button", { name: /Created at/ }).click();
    await expect(
      versions
        .getByLabel("Complete version authority")
        .getByText("Revision 1", { exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: retainedVersionScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });
    await versions
      .getByRole("button", { name: "Close version history" })
      .click();

    const search = page.getByPlaceholder("Search names and extracted text");
    await search.fill("Synthetic");
    await search.press("Enter");
    await expect(
      page.getByRole("cell", { name: "content", exact: true }).first(),
    ).toBeVisible();
    await page.screenshot({
      path: searchResultsScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });
    await search.fill("");
    await search.press("Enter");
    await expect(report).toBeVisible();
    await page.getByRole("button", { name: "Back to previous directory" }).click();
    await expect(
      page.getByRole("cell", { name: "Reports", exact: true }),
    ).toBeVisible();

    await page.getByRole("button", { name: "Storage status" }).click();
    const storage = page.getByRole("dialog", {
      name: "Physical storage status",
    });
    await expect(storage).toContainText("2 physical locations");
    await expect(storage).toContainText("archive");
    await expect(storage).toContainText("Sole copies");
    await page.screenshot({
      path: storageScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });
    await storage
      .getByRole("button", { name: "Close storage status" })
      .click();

    await runDocbank(["storage", "pack", "--json"]);
    await page.getByRole("button", { name: "Storage status" }).click();
    const packedStorage = page.getByRole("dialog", {
      name: "Physical storage status",
    });
    await expect(packedStorage).toContainText("1 immutable pack contains");
    await page.screenshot({
      path: packedStorageScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });
    await packedStorage
      .getByRole("button", { name: "Close storage status" })
      .click();

    await page
      .getByRole("button", { name: "Verify permanent audit evidence" })
      .click();
    const auditEvidence = page.getByRole("dialog", {
      name: "Permanent audit verification",
    });
    await expect(auditEvidence).toContainText(
      "Protected history and content agree",
    );
    await expect(auditEvidence).toContainText("1 protected scope");
    await page.screenshot({
      path: auditEvidenceScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });
    await auditEvidence
      .getByRole("button", { name: "Close permanent audit verification" })
      .click();

    await page.getByRole("button", { name: "Manage tag definitions" }).click();
    const catalog = page.getByRole("dialog", {
      name: "Manage tag definitions",
    });
    await expect(catalog).toContainText("tax");
    await expect(catalog).toContainText("reviewed");
    await catalog.getByRole("textbox", { name: "New tag name" }).fill("archived");
    await catalog.getByRole("button", { name: "Create" }).click();
    await expect(catalog).toContainText("Created archived.");
    await page.screenshot({
      path: tagCatalogScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });
    await catalog.getByRole("button", { name: "Done" }).click();

    await page.getByRole("cell", { name: "Reports", exact: true }).dblclick();
    const selectedReport = page.getByRole("cell", {
      name: "quarterly-tax-report.txt",
      exact: true,
    });
    await expect(selectedReport).toBeVisible();
    await selectedReport.click();

    await page.getByRole("button", { name: "Manage", exact: true }).click();
    const tags = page.getByRole("dialog", {
      name: "Manage tags for quarterly-tax-report.txt",
    });
    await expect(tags).toContainText("tax");
    await tags.getByRole("combobox", { name: "Tag to assign: Choose a tag…" }).click();
    await tags.getByRole("option", { name: "reviewed (0)" }).click();
    await tags.getByRole("button", { name: "Add tag" }).click();
    await expect(tags).toContainText("Added reviewed.");
    await page.screenshot({
      path: tagAssignmentScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });
    await tags.getByRole("button", { name: "Done" }).click();

    await page.getByRole("button", { name: "Move to trash", exact: true }).click();

    const confirmation = page.getByRole("dialog", {
      name: "Move quarterly-tax-report.txt to trash",
    });
    await expect(confirmation).toBeVisible();
    await expect(confirmation).toContainText("It remains recoverable from trash.");
    await expect(confirmation).toContainText(
      "This does not empty trash, reclaim stored bytes, or erase permanent audited history.",
    );
    await page.screenshot({
      path: screenshotPath,
      fullPage: true,
      animations: "disabled",
    });

    await confirmation.getByRole("button", { name: "Move to trash" }).click();
    await expect(confirmation).not.toBeVisible();
    await page.getByRole("button", { name: "Recoverable trash" }).click();
    const trash = page.getByRole("dialog", { name: "Recoverable trash" });
    await expect(trash).toContainText("quarterly-tax-report.txt");
    await trash.getByRole("button", { name: "Restore" }).click();

    const restore = page.getByRole("dialog", {
      name: "Restore quarterly-tax-report.txt from trash",
    });
    await expect(restore).toBeVisible();
    await expect(restore).toContainText(
      "It does not roll back versions or alter permanent audited history.",
    );
    await page.screenshot({
      path: restoreScreenshotPath,
      fullPage: true,
      animations: "disabled",
    });
  });

  test("TUI storage operations", async ({ page }) => {
    const socket = `docbank-screenshot-${process.pid}`;
    const session = "docbank-tui";
    const tmuxEnv = {
      ...process.env,
      DOCBANK_HOME: vault,
      DOCBANK_SCREENSHOT_BINARY: binary,
      TERM: "xterm-256color",
    };
    const tmux = async (args: string[]): Promise<string> => {
      const result = await execFileAsync("tmux", ["-L", socket, ...args], {
        cwd: repositoryRoot,
        env: tmuxEnv,
        maxBuffer: 1024 * 1024,
        timeout: 15_000,
      });
      return result.stdout;
    };
    const capture = async (): Promise<string> =>
      tmux(["capture-pane", "-p", "-t", session, "-S", "0"]);
    await tmux([
      "new-session",
      "-d",
      "-x",
      "120",
      "-y",
      "38",
      "-s",
      session,
      'exec "$DOCBANK_SCREENSHOT_BINARY" tui',
    ]);
    try {
      await expect
        .poll(capture, { timeout: 15_000 })
        .toContain("documents for you and your agents");
      await tmux(["send-keys", "-t", session, "O"]);
      const terminal = await expect
        .poll(capture, { timeout: 15_000 })
        .toContain("Vault operations")
        .then(capture);

      await page.setViewportSize({ width: 1280, height: 760 });
      await page.setContent(`
        <!doctype html>
        <meta charset="utf-8">
        <style>
          html, body { margin: 0; background: #07100f; }
          .terminal {
            box-sizing: border-box;
            width: max-content;
            min-width: 100vw;
            min-height: 100vh;
            margin: 0;
            padding: 16px 18px;
            color: #e8f1ef;
            background: #07100f;
            font: 16px/1.25 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
            white-space: pre;
          }
        </style>
        <pre class="terminal"></pre>
      `);
      await page.locator(".terminal").evaluate((element, text) => {
        element.textContent = String(text);
      }, terminal);
      await page.screenshot({
        path: tuiStorageScreenshotPath,
        fullPage: true,
        animations: "disabled",
      });
    } finally {
      await tmux(["kill-server"]).catch(() => undefined);
    }
  });
});
