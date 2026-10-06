// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// i18next-cli: one JSON file per language with hierarchical keys and i18next's plural suffixes. make lint runs
// `extract --ci --dry-run` (the files hold exactly the keys the code uses) and `status` (every key has a text in
// both languages, the Russian plural forms included).

import { defineConfig } from "i18next-cli";

export default defineConfig({
  locales: ["en", "ru"],
  extract: {
    input: ["src/**/*.{ts,tsx}"],
    ignore: ["src/api/gen/**", "src/**/*.test.{ts,tsx}"],
    output: "src/locales/{{language}}.json",
    defaultNS: false,
    keySeparator: ".",
    nsSeparator: false,
    primaryLanguage: "en",
    sort: true,
    indentation: 2,
    removeUnusedKeys: true,
    extractFromComments: false,
  },
});
