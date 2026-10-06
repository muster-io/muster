// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Component tests in Vitest browser mode: real Chromium through Playwright (ADR-0009). Screenshots of failures go
// under bin/, which git ignores.

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { playwright } from "@vitest/browser-playwright";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  test: {
    include: ["src/**/*.test.{ts,tsx}"],
    attachmentsDir: "../bin/vitest-attachments",
    browser: {
      enabled: true,
      headless: true,
      provider: playwright(),
      instances: [{ browser: "chromium" }],
    },
  },
});
