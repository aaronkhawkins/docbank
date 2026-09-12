import { expect, test } from "@playwright/test";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

const exec = promisify(execFile);
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const screenshots = path.join(root, ".superpowers/screenshots");

// The full browser includes the native PDF viewer. Disable its OOPIF path in
// this capture harness: Chromium otherwise clips the plugin surface to 300px.
test.use({
  channel: "chromium",
  launchOptions: { args: ["--disable-features=PdfOopif"] },
});

test.describe("Original document previews", () => {
  let workspace = "";
  let vault = "";
  const originals = new Map<string, Buffer>();
  const documents = new Map<string, { node: number; version: string }>();

  async function docbank(args: string[]) {
    const result = await exec(path.join(root, "docbank"), args, {
      cwd: root,
      env: { ...process.env, DOCBANK_HOME: vault },
      timeout: 60_000,
    });
    return result.stdout.trim();
  }

  test.beforeAll(async ({ browser }) => {
    workspace = await mkdtemp(path.join(tmpdir(), "docbank-preview-"));
    vault = path.join(workspace, "vault");
    await mkdir(vault, { mode: 0o700 });
    await mkdir(screenshots, { recursive: true });
    // Render a synthetic source document, then import its actual PDF and PNG
    // bytes into a temporary real vault. The viewer API is never mocked.
    const source = await browser.newPage({ viewport: { width: 780, height: 680 } });
    try {
      await source.setContent(`<!doctype html><html lang="en"><title>Synthetic vehicle registration</title><style>
        body { margin: 0; padding: 56px; background: white; color: #172c40;
          font: 18px/1.6 Arial, sans-serif; }
        h1 { font-size: 30px; margin-bottom: 0; }
        .label { color: #587185; font-size: 13px; letter-spacing: 2px; }
        table { width: 100%; border-collapse: collapse; margin: 36px 0; }
        td { padding: 14px 0; border-bottom: 1px solid #dce4ea; }
        td:first-child { width: 45%; color: #587185; }
        footer { font-size: 13px; border-top: 3px solid #217781; padding-top: 20px; }
      </style><body><div class="label">SYNTHETIC DOCUMENT · PREVIEW TEST</div>
        <h1>Vehicle registration</h1><p>Example County · Document services</p>
        <table><tr><td>Vehicle</td><td>2024 Example Motors Comet</td></tr>
          <tr><td>Registration</td><td>DEMO-482</td></tr>
          <tr><td>Valid through</td><td>September 30, 2027</td></tr>
          <tr><td>Document owner</td><td>Sample household</td></tr></table>
        <footer>Fictional data for testing DocBank original-document previews.</footer>
      </body></html>`);
      originals.set("registration.pdf", await source.pdf({ format: "Letter", printBackground: true }));
      originals.set("registration.png", await source.screenshot({ fullPage: true }));
    } finally {
      await source.close();
    }
    for (const [name, bytes] of originals) {
      const file = path.join(workspace, name);
      await writeFile(file, bytes, { mode: 0o600 });
      await docbank(["add", file, "--dest", "/", "--progress", "plain"]);
      const node = JSON.parse(await docbank(["stat", `/${name}`, "--json"]));
      const versions = JSON.parse(await docbank(["versions", "list", `/${name}`, "--json"]));
      documents.set(name, { node: node.id, version: versions.items[0].id });
    }
  });

  test.afterAll(async () => {
    if (vault) {
      await docbank(["daemon", "stop"]);
      const status = JSON.parse(await docbank(["daemon", "status", "--json"]));
      if (status.running !== false) throw new Error(`Preview daemon still running: ${workspace}`);
    }
    if (workspace) await rm(workspace, { recursive: true, force: true });
  });

  for (const [name, width] of [["registration.pdf", 1440], ["registration.png", 390]] as const) {
    test(`${name} at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 960 });
      // Observe the real blob handed to the renderer. Chromium's inspector
      // can omit attachment response bodies; no API response is substituted.
      await page.addInitScript(() => {
        const createObjectURL = URL.createObjectURL.bind(URL);
        URL.createObjectURL = (value) => {
          if (value instanceof Blob) {
            (window as Window & { previewBlob?: Blob }).previewBlob = value;
          }
          return createObjectURL(value);
        };
      });
      const errors: string[] = [];
      page.on("pageerror", error => errors.push(error.message));
      page.on("console", message => {
        if (message.type() === "error" && !message.location().url.endsWith("/favicon.ico")) {
          errors.push(`${message.location().url}: ${message.text()}`);
        }
      });
      const selected = documents.get(name)!;
      const url = new URL(await docbank(["web", "--no-browser"]));
      url.pathname = `/documents/${selected.node}/versions/${selected.version}`;
      const bytesResponse = page.waitForResponse(response => response.url().includes("/api/daemon/web-download/file?ticket="));
      await page.goto(url.toString());
      await expect(page.getByRole("tab", { name: "Original", exact: true })).toHaveAttribute("aria-selected", "true");
      const response = await bytesResponse;
      expect(response.ok()).toBe(true);
      const hash = (bytes: Buffer) => createHash("sha256").update(bytes).digest("hex");
      if (name.endsWith(".pdf")) {
        await expect(page.locator("iframe")).toBeVisible();
        await expect(page.locator("iframe")).toHaveAttribute("src", /^blob:/);
        // Native PDF painting happens inside a closed browser shadow tree.
        await page.waitForTimeout(3000);
      } else {
        const preview = page.locator("img");
        await expect(preview).toBeVisible();
        await expect.poll(() => preview.evaluate(image => (image as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
      }
      const previewHash = await page.evaluate(async () => {
        const bytes = await (window as Window & { previewBlob?: Blob }).previewBlob!.arrayBuffer();
        const digest = await crypto.subtle.digest("SHA-256", bytes);
        return Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("");
      });
      expect(previewHash).toBe(hash(originals.get(name)!));
      await expect(page.locator("details")).not.toHaveAttribute("open", "");
      await page.screenshot({ path: path.join(screenshots, `document-preview-${name.endsWith(".pdf") ? "pdf-desktop" : "image-mobile"}.png`), fullPage: true });
      await page.getByRole("tab", { name: "OCR", exact: true }).click();
      await expect(page.getByRole("tab", { name: "OCR", exact: true })).toHaveAttribute("aria-selected", "true");
      await expect(page.getByRole("button", { name: /Download original/ })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      expect(errors).toEqual([]);
    });
  }
});
