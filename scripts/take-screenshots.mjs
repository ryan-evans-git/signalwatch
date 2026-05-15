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
  // Subscriptions is its own top-level tab; the screenshot surfaces
  // the dwell/repeat/resolve columns plus the Mode pill (one-shot vs
  // recurring) added in the post-v0.4 one-shot-subscriptions feature.
  { name: "subscriptions", tab: "Subscriptions" },
  { name: "states", tab: "Live State" },
  { name: "incidents", tab: "Incidents" },
];

// Hash-routed deep links to capture in addition to the top-level tabs.
// Each entry navigates to a hash and screenshots the resulting page.
const HASH_SHOTS = [
  { name: "rule-detail", hash: "#/rules/r-orders" },
];

const browser = await chromium.launch();
try {
  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 2, // crisp PNGs on retina
  });
  // If the driver script set SW_TOKEN, prime the UI's localStorage so
  // the SPA renders authenticated and we capture the data tabs rather
  // than the login gate. addInitScript runs in every fresh document
  // BEFORE any of the SPA's own JS, so the auth helper sees the
  // token on first read.
  const swToken = process.env.SW_TOKEN ?? "";
  if (swToken) {
    await context.addInitScript((t) => {
      try {
        window.localStorage.setItem("signalwatch.api_token", t);
      } catch {
        // Some test contexts disable storage; fall through and let
        // the SPA render its login gate.
      }
    }, swToken);
  }
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

  for (const shot of HASH_SHOTS) {
    await page.goto(BASE + "/" + shot.hash, { waitUntil: "networkidle" });
    // The hash-routed page fans out several requests (rule + incidents
    // + per-incident notifications). networkidle covers most of it,
    // but give React a beat for the final render pass.
    await page.waitForTimeout(400);
    const out = resolve(OUT_DIR, `${shot.name}.png`);
    await page.screenshot({ path: out, fullPage: false });
    console.log(`wrote ${out}`);
  }
} finally {
  await browser.close();
}
