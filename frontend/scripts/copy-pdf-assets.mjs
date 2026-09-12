import { cp, mkdir, readFile, rm } from "node:fs/promises";

// Keep the renderer's fonts and image decoders with the app, never on a CDN.
const source = new URL("../node_modules/pdfjs-dist/", import.meta.url);
const { version } = JSON.parse(await readFile(new URL("package.json", source), "utf8"));
const root = new URL("../public/assets/pdfjs/", import.meta.url);
await rm(root, { recursive: true, force: true });
const target = new URL(`${version}/`, root);
await mkdir(target, { recursive: true });
for (const directory of ["cmaps", "standard_fonts", "wasm"]) {
  await cp(new URL(directory, source), new URL(directory, target), { recursive: true });
}
await cp(new URL("LICENSE", source), new URL("LICENSE", target));
