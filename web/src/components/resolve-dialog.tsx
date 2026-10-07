// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Resolve dialog (C-10.FR-1) with its optional Note, sent with the Command, and the Unresolve confirmation
// (C-10.FR-7): "Bring this Alert Group back as firing without an Owner?". A phone shows them as full-screen sheets. A
// refusal shows inside the dialog, which stays open; a success closes it.

import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { SHEET } from "../lib/commands";
import { NoteField, noteValid } from "./note-box";
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

interface CommandDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  pending: boolean;
  /** The refusal of the last attempt, shown in the dialog. */
  error?: ReactNode;
}

function ResolveForm({
  pending,
  error,
  submitLabel,
  onResolve,
}: {
  pending: boolean;
  error?: ReactNode;
  submitLabel: string;
  onResolve: (note: string | undefined) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const [note, setNote] = useState("");
  const empty = note.trim() === "";
  const valid = empty || noteValid(note);
  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid && !pending) {
          onResolve(empty ? undefined : note);
        }
      }}
    >
      <NoteField
        id={`${id}-note`}
        label={t("commands.resolve.note")}
        value={note}
        onChange={setNote}
        disabled={pending}
      />
      {error}
      <DialogFooter>
        <DialogClose render={<Button variant="outline" type="button" />}>
          {t("common.cancel")}
        </DialogClose>
        <Button type="submit" disabled={!valid || pending} aria-busy={pending}>
          {submitLabel}
        </Button>
      </DialogFooter>
    </form>
  );
}

/** Resolve with an optional Note; Resolve of a selection in the list uses it too. */
export function ResolveDialog({
  open,
  onOpenChange,
  title,
  pending,
  error,
  onResolve,
}: CommandDialogProps & {
  onResolve: (note: string | undefined) => void;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")} className={SHEET} data-testid="resolve-dialog">
        <DialogHeader>
          <DialogTitle className="pr-8 wrap-anywhere">{title}</DialogTitle>
          <DialogDescription>{t("commands.resolve.description")}</DialogDescription>
        </DialogHeader>
        <ResolveForm
          pending={pending}
          error={error}
          submitLabel={t("commands.buttons.resolve")}
          onResolve={onResolve}
        />
      </DialogContent>
    </Dialog>
  );
}

/** The confirmation of Unresolve. */
export function UnresolveDialog({
  open,
  onOpenChange,
  title,
  pending,
  error,
  onConfirm,
}: CommandDialogProps & { onConfirm: () => void }) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        closeLabel={t("common.close")}
        className={SHEET}
        data-testid="unresolve-dialog"
      >
        <DialogHeader>
          <DialogTitle className="pr-8 wrap-anywhere">{title}</DialogTitle>
          <DialogDescription>{t("commands.unresolve.question")}</DialogDescription>
        </DialogHeader>
        {error}
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
          <Button disabled={pending} aria-busy={pending} onClick={onConfirm}>
            {t("commands.buttons.unresolve")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
