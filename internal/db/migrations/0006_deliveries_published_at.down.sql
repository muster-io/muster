-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0006_deliveries_published_at.up.sql: nothing to undo. The rows it cleared had no Root message; the earlier
-- binary reads them by their message id.

SELECT 1;
