// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The documentation screenshots (make docs-screenshots): the specs under e2e/screenshots/, against the `muster dev` of
// the same global setup as make e2e, in a 1280 × 800 viewport at device scale 1, in English and the light colour
// scheme, without trace or video. The images go under bin/docs-screenshots/images/ (e2e/screenshots/capture.ts), the
// test output under bin/docs-screenshots/results/. One run takes every spec but the clock specs, so it may last 45
// minutes. It refuses to load outside make docs-screenshots, which runs it in the pinned Playwright image so that the
// fonts of every run are the same.

import { defineConfig } from "@playwright/test";

import base from "./playwright.config";

if (process.env.MUSTER_DOCS_SCREENSHOTS !== "1") {
  throw new Error(
    "playwright.screenshots.config.ts runs only through make docs-screenshots, in the pinned Playwright image on Linux with Docker, so that fonts match",
  );
}

export default defineConfig({
  ...base,
  testDir: "e2e/screenshots",
  testIgnore: [],
  outputDir: "../bin/docs-screenshots/results",
  globalTimeout: 45 * 60_000,
  use: {
    ...base.use,
    viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 1,
    colorScheme: "light",
    locale: "en-US",
    trace: "off",
    video: "off",
    screenshot: "off",
    // Chromium draws rounded borders the same way on every run only without its run-time Skia optimizations and
    // partial or GPU rasterization; the colour profile is fixed as well.
    launchOptions: {
      args: [
        "--disable-skia-runtime-opts",
        "--disable-partial-raster",
        "--disable-gpu-rasterization",
        "--force-color-profile=srgb",
      ],
    },
  },
});
