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
    // Every page loads on demand except the sign-in page, the first one a visitor without a session sees.
    tanstackRouter({
      target: "react",
      autoCodeSplitting: true,
      codeSplittingOptions: {
        splitBehavior: ({ routeId }) => (routeId === "/sign-in" ? [] : undefined),
      },
    }),
    react(),
    tailwindcss(),
    keepGitkeep(),
  ],
  build: {
    rolldownOptions: {
      // The generated client, schemas and models only declare things, so a module of theirs that nothing uses is left
      // out; a route's eager part may still import one for a form of its lazily loaded page.
      treeshake: {
        moduleSideEffects: (id) => (/[\\/]src[\\/]api[\\/]gen[\\/]/.test(id) ? false : undefined),
        manualPureFunctions: ["zod"],
      },
      output: {
        // React in a chunk of its own: it changes with dependency updates only, so browsers keep it across releases.
        codeSplitting: {
          groups: [
            { name: "react", test: /[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/ },
          ],
        },
      },
    },
  },
});
