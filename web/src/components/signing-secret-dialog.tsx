// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Signing secret of an outgoing webhook, shown once (C-15.FR-5): after the Destination is created and after
// "Regenerate". The value lives only in the state of the page that asked for it, never in a query cache: closing the
// dialog drops it, and Muster never shows it again. It follows the token dialogs: a read-only field, a copy button and
// the warning that it is shown once; a click outside does not close it, "Close" and Escape do.

import { CheckIcon, CopyIcon, TriangleAlertIcon } from "lucide-react";
import { type RefObject, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { COPIED_MS, copyField } from "./setup-link-dialog";
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
import { Input } from "./ui/input";
import { Label } from "./ui/label";

export interface SigningSecretDialogProps {
  /** The secret to show; the dialog is open while it is set. */
  secret: string | null;
  onClose: () => void;
  /** The element that takes the focus once the dialog closes, such as "Regenerate". */
  finalFocus?: RefObject<HTMLElement | null>;
}

export function SigningSecretDialog({ secret, onClose, finalFocus }: SigningSecretDialogProps) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const field = useRef<HTMLInputElement>(null);
  return (
    <Dialog
      open={secret !== null}
      disablePointerDismissal
      onOpenChange={(open) => {
        if (!open) {
          setCopied(false);
          onClose();
        }
      }}
    >
      <DialogContent
        finalFocus={finalFocus}
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg"
        data-testid="signing-secret-dialog"
      >
        <DialogHeader>
          <DialogTitle>{t("destinations.signing.dialogTitle")}</DialogTitle>
          <DialogDescription>{t("destinations.signing.dialogHint")}</DialogDescription>
        </DialogHeader>
        {secret !== null && (
          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-2">
              <Label htmlFor="signing-secret-value">{t("destinations.signing.value")}</Label>
              <div className="flex flex-col gap-2 sm:flex-row">
                <Input
                  id="signing-secret-value"
                  readOnly
                  value={secret}
                  className="font-mono text-xs"
                  spellCheck={false}
                  autoComplete="off"
                  onFocus={(e) => e.currentTarget.select()}
                  ref={field}
                  autoFocus
                  data-testid="signing-secret-value"
                />
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    void copyField(secret, field.current).then((done) => {
                      setCopied(done);
                      if (done) {
                        setTimeout(() => setCopied(false), COPIED_MS);
                      }
                    });
                  }}
                >
                  {copied ? <CheckIcon aria-hidden="true" /> : <CopyIcon aria-hidden="true" />}
                  {copied ? t("setupLink.copied") : t("setupLink.copy")}
                </Button>
                <span role="status" className="sr-only">
                  {copied ? t("setupLink.copied") : ""}
                </span>
              </div>
            </div>
            <p className="flex items-start gap-1.5 text-sm font-medium">
              <TriangleAlertIcon
                aria-hidden="true"
                className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
              />
              <span>{t("destinations.signing.once")}</span>
            </p>
          </div>
        )}
        <DialogFooter>
          <DialogClose render={<Button />}>{t("common.close")}</DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
