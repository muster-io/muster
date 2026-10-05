// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

import { defineConfig } from "orval";

const input = { target: "../api/openapi.yaml" };

// Two outputs, each in a directory of its own because clean empties the directories it writes to. The output is
// written raw, without a formatter, so that the same spec always gives the same files.
export default defineConfig({
  client: {
    input,
    output: {
      mode: "tags-split",
      target: "src/api/gen/endpoints/muster.ts",
      schemas: "src/api/gen/model",
      client: "react-query",
      httpClient: "fetch",
      baseUrl: "/api/v1",
      mock: true,
      clean: true,
    },
  },
  zod: {
    input,
    output: {
      mode: "tags-split",
      target: "src/api/gen/zod/muster.ts",
      client: "zod",
      fileExtension: ".zod.ts",
      clean: true,
    },
  },
});
