// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The README demo (make demo): e2e/demo.spec.ts alone, against the `muster dev` of the same global setup, recorded as
// a 1280 × 720 video under bin/demo, which make demo converts into docs/assets/demo.gif. It runs by hand, never in CI.

import { defineConfig } from "@playwright/test";

import base from "./playwright.config";

export default defineConfig({
  ...base,
  testMatch: "demo.spec.ts",
  testIgnore: [],
  outputDir: "../bin/demo",
  timeout: 180_000,
  use: {
    ...base.use,
    viewport: { width: 1280, height: 720 },
    video: { mode: "on", size: { width: 1280, height: 720 } },
    trace: "off",
    screenshot: "off",
  },
});
