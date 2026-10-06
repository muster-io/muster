// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The filters of the Stored Snapshots in the URL of the Integration page. They live apart from the table: the route
// validates its search parameters before its page loads, so whatever this module imports loads with the application.

import { z } from "zod";

import { SnapshotState } from "../api/gen/model";

/** A day as the date inputs give it, yyyy-mm-dd. */
export const DAY = /^\d{4}-\d{2}-\d{2}$/;

export const snapshotSearchSchema = z.object({
  snapshot_state: z.enum(SnapshotState).optional().catch(undefined),
  snapshot_from: z.string().regex(DAY).optional().catch(undefined),
  snapshot_to: z.string().regex(DAY).optional().catch(undefined),
});
export type SnapshotSearch = z.infer<typeof snapshotSearchSchema>;
