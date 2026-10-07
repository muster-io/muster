// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The result of a bulk command (C-10.FR-14): every Alert Group with its outcome — "#N: done", "#N: {refusal}" or
// "#N: skipped: owned by {Owner}" — as one refusal never stops the others. A phone shows it as a full-screen sheet.

import { useTranslation } from "react-i18next";

import type { BulkCommandItem } from "../api/gen/model";
import { SHEET, bulkItemText } from "../lib/commands";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { cn } from "./ui/utils";

export interface BulkResult {
  /** The name of the Command as its button reads. */
  command: string;
  items: BulkCommandItem[];
  /** The Owners of the Alert Groups as the list showed them, for "skipped: owned by {Owner}". */
  owners: ReadonlyMap<string, string>;
}

export function BulkResultDialog({
  result,
  onClose,
}: {
  result: BulkResult | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const items = result?.items ?? [];
  const done = items.filter((i) => i.outcome === "done" || i.outcome === "unchanged").length;
  return (
    <Dialog
      open={result !== null}
      onOpenChange={(open) => {
        if (!open) {
          onClose();
        }
      }}
    >
      <DialogContent closeLabel={t("common.close")} className={SHEET} data-testid="bulk-result">
        <DialogHeader>
          <DialogTitle className="pr-8">
            {t("commands.bulk.resultTitle", { command: result?.command ?? "" })}
          </DialogTitle>
          <DialogDescription>
            {t("commands.bulk.resultSummary", { count: items.length, done })}
          </DialogDescription>
        </DialogHeader>
        <ul className="flex max-h-[60dvh] flex-col gap-1 overflow-y-auto text-sm max-sm:max-h-none">
          {items.map((item) => {
            const ok = item.outcome === "done" || item.outcome === "unchanged";
            return (
              <li
                key={item.alert_group_id}
                className={cn("wrap-anywhere", !ok && "text-muted-foreground")}
                data-testid="bulk-result-item"
                data-outcome={item.outcome}
              >
                {bulkItemText(t, item, result?.owners.get(item.alert_group_id))}
              </li>
            );
          })}
        </ul>
        <DialogFooter>
          <DialogClose render={<Button />}>{t("common.close")}</DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
