// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Shows a password setup link once (C-03.FR-3): after a user is created, converted to local or given a new link. Muster
// sends no email, so the Admin copies the link and hands it over; it is not shown again.

import { CheckIcon, CopyIcon } from "lucide-react";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { PasswordSetupLink } from "../api/gen/model";
import { useTimeFormat } from "../lib/time";
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

/** How long a copy button says "Copied". */
export const COPIED_MS = 3000;

/**
 * Copies the text of a read-only field: through the clipboard in a secure context, else (a plain-http installation) by
 * selecting the field and the browser's copy command. Resolves whether it worked; the field stays selected for a
 * manual copy either way. The token dialog shares it.
 */
export async function copyField(text: string, field: HTMLInputElement | null): Promise<boolean> {
  field?.select();
  if (window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      return false;
    }
  }
  // execCommand is deprecated, but it is the only copy outside a secure context.
  return document.execCommand("copy");
}

/** The body of the dialog, also used by the dialog that created the user. */
export function SetupLinkContent({
  link,
  userName,
}: {
  link: PasswordSetupLink;
  userName: string;
}) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const [copied, setCopied] = useState(false);
  const field = useRef<HTMLInputElement>(null);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-2">
        <Label htmlFor="setup-link-url">{t("setupLink.url")}</Label>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Input
            id="setup-link-url"
            readOnly
            value={link.url}
            className="font-mono text-xs"
            spellCheck={false}
            onFocus={(e) => e.currentTarget.select()}
            ref={field}
            autoFocus
            data-testid="setup-link-url"
          />
          <Button
            variant="outline"
            onClick={() => {
              void copyField(link.url, field.current).then((done) => {
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
      <p className="text-sm">
        {t("setupLink.handOver", { name: userName, time: dateTime(link.expires_at) })}
      </p>
      <p className="text-sm font-medium">{t("setupLink.shownOnce")}</p>
    </div>
  );
}

export function SetupLinkDialog({
  link,
  userName,
  onClose,
}: {
  link: PasswordSetupLink | null;
  userName: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Dialog
      open={link !== null}
      onOpenChange={(open) => {
        if (!open) {
          onClose();
        }
      }}
    >
      <DialogContent closeLabel={t("common.close")} className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("setupLink.title")}</DialogTitle>
          <DialogDescription>{t("setupLink.hint")}</DialogDescription>
        </DialogHeader>
        {link !== null && <SetupLinkContent link={link} userName={userName} />}
        <DialogFooter>
          <DialogClose render={<Button />}>{t("common.done")}</DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
