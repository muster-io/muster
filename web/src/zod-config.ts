// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Zod compiles object parsers with new Function unless it runs jitless, and it decides when a schema is built, after
// probing with a caught new Function that the Content Security Policy (no 'unsafe-eval') still reports as a
// violation. main.tsx imports this module first, so the setting holds before any schema exists.

import { config } from "zod";

config({ jitless: true });
