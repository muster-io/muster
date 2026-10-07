// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "N new" above the Alert Group list (C-09.FR-25): Alert Groups that newly match the view are announced instead of
// pushing the rows down under the pointer, and shown when it is pressed. The live region keeps the place of the
// button, so the announcement is read out once when it appears or changes.

import { ArrowUpIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "./ui/button";

export function NewAlertGroupsBanner({ count, onShow }: { count: number; onShow: () => void }) {
  const { t } = useTranslation();
  return (
    <div role="status" aria-live="polite" className="empty:hidden">
      {count > 0 && (
        <Button
          variant="secondary"
          className="w-full sm:w-auto"
          onClick={onShow}
          aria-label={t("alertGroups.newCountLabel", { count })}
          data-testid="new-alert-groups"
        >
          <ArrowUpIcon aria-hidden="true" />
          {t("alertGroups.newCount", { count })}
        </Button>
      )}
    </div>
  );
}
