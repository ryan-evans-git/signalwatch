#!/usr/bin/env node
// Take README screenshots of the bundled signalwatch UI.
//
// Prerequisites: a built signalwatch binary listening on
// http://127.0.0.1:18080 with seeded data. The companion shell driver
// in scripts/take-screenshots.sh handles that orchestration; you can
// also point this script at any running instance via SW_BASE_URL.
//
// Usage:
//   node scripts/take-screenshots.mjs
//
// Output:
//   docs/screenshots/{rules,states,subscribers,incidents}.png
//
// Why Playwright (vs the puppeteer/headless-chrome family): it ships
// with browser-version pinning, has a stable CI story (`npx
// playwright install --with-deps chromium`), and the API is simpler
// for the small handful of pages we shoot.

import { chromium } from "playwright";
import { mkdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const OUT_DIR = resolve(__dirname, "..", "docs", "screenshots");
const BASE = process.env.SW_BASE_URL ?? "http://127.0.0.1:18080";

mkdirSync(OUT_DIR, { recursive: true });

const SHOTS = [
  { name: "rules", tab: "Rules" },
  { name: "subscribers", tab: "Subscribers" },
  { name: "states", tab: "Live State" },
  { name: "incidents", tab: "Incidents" },
];

const browser = await chromium.launch();
try {
  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 2, // crisp PNGs on retina
  });
  const page = await context.newPage();

  for (const shot of SHOTS) {
    await page.goto(BASE, { waitUntil: "networkidle" });
    // Click the named tab. The SPA is one component so the click is
    // synchronous; networkidle waits out the subsequent list fetch.
    await page.getByRole("button", { name: shot.tab }).click();
    await page.waitForLoadState("networkidle");
    // Brief wait for React's render flush — without it, the
    // active-tab underline occasionally captures one frame behind.
    await page.waitForTimeout(150);
    const out = resolve(OUT_DIR, `${shot.name}.png`);
    await page.screenshot({ path: out, fullPage: false });
    console.log(`wrote ${out}`);
  }
} finally {
  await browser.close();
}
