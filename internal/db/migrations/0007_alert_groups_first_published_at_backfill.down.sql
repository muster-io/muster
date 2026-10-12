-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0007_alert_groups_first_published_at_backfill.up.sql: nothing to undo. The filled times are the ones the
-- binary would have set at the first Publication; clearing them could not tell them apart from those it set itself,
-- and the earlier binary does not read the column.

SELECT 1;
