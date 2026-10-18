// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The tab and the filters of the Request log in the URL of the Connection page and of the outgoing webhook Destination
// page (C-23.FR-6), so that a link to a filtered list can be shared. They live apart from the list: the routes validate
// their search parameters before their pages load, so whatever this module imports loads with the application.

import { z } from "zod";

import { ListRequestLogStatusClass, RequestDirection } from "../api/gen/model";
import {
  listRequestLogQueryOperationMax,
  listRequestLogQueryRunRegExp,
} from "../api/gen/zod/request-log/request-log.zod";

/** The tabs of a page with a Request log; Settings when the URL names none. */
export const PAGE_TABS = ["settings", "request-log"] as const;
export type PageTab = (typeof PAGE_TABS)[number];

export const requestLogSearchSchema = z.object({
  tab: z.enum(["request-log"]).optional().catch(undefined),
  direction: z.enum(RequestDirection).optional().catch(undefined),
  status_class: z.enum(ListRequestLogStatusClass).optional().catch(undefined),
  operation: z.string().min(1).max(listRequestLogQueryOperationMax).optional().catch(undefined),
  run: z.string().regex(listRequestLogQueryRunRegExp).optional().catch(undefined),
});
export type RequestLogSearch = z.infer<typeof requestLogSearchSchema>;

/** The filters of a Request log: the search parameters without the tab. */
export type RequestLogFilters = Omit<RequestLogSearch, "tab">;
