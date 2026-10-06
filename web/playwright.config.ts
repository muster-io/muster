// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Playwright end-to-end specs against `muster dev` (make e2e), which e2e/global-setup.ts starts on a fresh
// database. The specs share the server and its fixed ports, so they run one at a time. Output goes under bin/, which
// git ignores.

import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "e2e",
  globalSetup: "./e2e/global-setup.ts",
  outputDir: "../bin/e2e-web",
  fullyParallel: false,
  workers: 1,
  forbidOnly: process.env.CI !== undefined,
  retries: 0,
  timeout: 90_000,
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
