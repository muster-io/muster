// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The filters of the Alerts view in the URL of the Integration page. They live apart from the view: the route
// validates its search parameters before its page loads, so whatever this module imports loads with the application.

import { z } from "zod";

import { ListIntegrationAlertsSort } from "../api/gen/model";

/** The state tabs of the Alerts view; Firing when the URL names none. */
export const ALERT_TABS = ["firing", "resolved", "all"] as const;
export type AlertTab = (typeof ALERT_TABS)[number];

export const alertSearchSchema = z.object({
  alerts_state: z.enum(ALERT_TABS).optional().catch(undefined),
  alerts_label: z.array(z.string()).optional().catch(undefined),
  alerts_q: z.string().optional().catch(undefined),
  alerts_sort: z.enum(ListIntegrationAlertsSort).optional().catch(undefined),
});
export type AlertSearch = z.infer<typeof alertSearchSchema>;
