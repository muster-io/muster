// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Playwright end-to-end specs against `muster dev` (make e2e), which e2e/global-setup.ts starts on a fresh
// database. The specs share the server and its fixed ports, so they run one at a time. Output goes under bin/, which
// git ignores. The README demo (e2e/demo.spec.ts) is left out: make demo records it with playwright.demo.config.ts; so
// are the documentation screenshots (e2e/screenshots/), which make docs-screenshots takes with
// playwright.screenshots.config.ts.

import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "e2e",
  testIgnore: ["demo.spec.ts", "**/screenshots/**"],
  globalSetup: "./e2e/global-setup.ts",
  outputDir: "../bin/e2e-web",
  fullyParallel: false,
  workers: 1,
  forbidOnly: process.env.CI !== undefined,
  retries: 0,
  timeout: 90_000,
  // The whole run ends within 15 minutes, before the 20 of the CI job, so that a run that hangs still reports which
  // specs were running instead of being cut off without its log.
  globalTimeout: 15 * 60_000,
  expect: { timeout: 10_000 },
  reporter: [["list"]],
  use: {
    ...devices["Desktop Chrome"],
    baseURL: "http://localhost:8080",
    viewport: { width: 1280, height: 800 },
    locale: "en-US",
    timezoneId: "Europe/Berlin",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
});
