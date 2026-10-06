// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// Vite empties dist/ before every build. The empty dist/.gitkeep is checked in so that the go:embed pattern of
// package web matches in a checkout where the SPA was never built, so the build emits it again.
function keepGitkeep(): Plugin {
  return {
    name: "muster:keep-gitkeep",
    apply: "build",
    generateBundle() {
      this.emitFile({ type: "asset", fileName: ".gitkeep", source: "" });
    },
  };
}

export default defineConfig({
  plugins: [
    tanstackRouter({ target: "react", autoCodeSplitting: true }),
    react(),
    tailwindcss(),
    keepGitkeep(),
  ],
});
