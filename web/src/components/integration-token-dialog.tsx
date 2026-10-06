// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Creating an Integration token (C-05.FR-2, FR-5): the dialog takes an optional name, then shows the value and the
// Alertmanager configuration that contains it once, each with a copy button. Both live only in this dialog's state:
// the request's result keeps neither, closing the dialog drops them, and the token list never shows a value.

import { useMutation } from "@tanstack/react-query";
import { CheckIcon, CopyIcon, TriangleAlertIcon } from "lucide-react";
import { type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { IntegrationTokenCreated } from "../api/gen/model";
import { fieldErrorText, isApiError, problemText } from "../lib/api";
import { COPIED_MS, copyField } from "./setup-link-dialog";
import { Alert, AlertDescription } from "./ui/alert";
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
import { cn } from "./ui/utils";

/** The longest name of a token, in characters, as the API checks it. */
const NAME_MAX = 200;

/**
 * Copies text exactly: through the clipboard in a secure context, else (a plain-http installation) with the browser's
 * copy command on a hidden field that holds the text, since the block may show only part of it or another view of it.
 */
async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext) {
    return copyField(text, null);
  }
  const field = document.createElement("textarea");
  field.value = text;
  field.readOnly = true;
  field.className = "sr-only";
  document.body.append(field);
  try {
    field.select();
    // execCommand is deprecated, but it is the only copy outside a secure context.
    return document.execCommand("copy");
  } finally {
    field.remove();
  }
}

/**
 * A labelled monospace block with "Copy", which copies text. children, when given, is what the block shows instead: a
 * part of the text or another view of it. actions are more buttons beside "Copy".
 */
export function CopyBlock({
  id,
  label,
  text,
  children,
  actions,
  wrap = true,
  testId,
  className,
}: {
  id: string;
  label: string;
  text: string;
  children?: ReactNode;
  actions?: ReactNode;
  /** Long lines wrap; without it they scroll inside the block, which keeps the indentation of YAML readable. */
  wrap?: boolean;
  testId?: string;
  className?: string;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span id={`${id}-label`} className="text-sm font-medium">
          {label}
        </span>
        <div className="flex flex-wrap items-center gap-2">
          {actions}
          <Button
            variant="outline"
            size="sm"
            aria-describedby={`${id}-label`}
            onClick={() => {
              void copyText(text).then((done) => {
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
        </div>
        <span role="status" className="sr-only">
          {copied ? t("setupLink.copied") : ""}
        </span>
      </div>
      {/* A focusable region, so that the keyboard can scroll the block. */}
      <pre
        id={id}
        role="region"
        tabIndex={0}
        aria-labelledby={`${id}-label`}
        className={cn(
          "min-w-0 overflow-auto rounded-lg border bg-muted/50 p-2.5 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring",
          wrap ? "whitespace-pre-wrap wrap-anywhere" : "whitespace-pre",
          className,
        )}
        data-testid={testId}
      >
        {children ?? text}
      </pre>
    </div>
  );
}

/** The value and the snippet of a created token, and the warning that they are shown once. */
function CreatedToken({ value, snippet }: { value: string; snippet: string }) {
  const { t } = useTranslation();
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <CopyBlock
        id="integration-token-value"
        label={t("tokens.created.value")}
        text={value}
        testId="integration-token-value"
      />
      <CopyBlock
        id="integration-token-snippet"
        label={t("integrations.tokens.created.snippet")}
        text={snippet}
        testId="integration-token-snippet"
        wrap={false}
        className="max-h-72"
      />
      <p className="flex items-start gap-1.5 text-sm font-medium">
        <TriangleAlertIcon
          aria-hidden="true"
          className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
        />
        <span>{t("tokens.created.once")}</span>
      </p>
    </div>
  );
}

function TokenForm({
  create,
  onCreated,
}: {
  create: (name: string) => Promise<IntegrationTokenCreated>;
  onCreated: (created: { value: string; snippet: string; name: string }) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  // The request's result keeps nothing: the value and the snippet go straight to the dialog's state.
  const submit = useMutation({
    mutationFn: async (tokenName: string) => {
      const created = await create(tokenName);
      onCreated({
        value: created.value,
        snippet: created.alertmanager_snippet,
        name: created.token.name ?? "",
      });
    },
  });
  const fieldError =
    isApiError(submit.error) && submit.error.errors?.length
      ? submit.error.errors.map((e) => fieldErrorText(t, e.code)).join(" ")
      : undefined;
  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        submit.mutate(name.trim());
      }}
    >
      <div className="flex flex-col gap-2">
        <Label htmlFor="integration-token-name">{t("tokens.fields.name")}</Label>
        <Input
          id="integration-token-name"
          autoComplete="off"
          spellCheck={false}
          maxLength={NAME_MAX}
          value={name}
          aria-invalid={fieldError !== undefined}
          aria-describedby={
            fieldError ? "integration-token-name-error" : "integration-token-name-hint"
          }
          onChange={(e) => setName(e.target.value)}
        />
        {fieldError ? (
          <p id="integration-token-name-error" className="text-sm text-destructive">
            {fieldError}
          </p>
        ) : (
          <p id="integration-token-name-hint" className="text-sm text-muted-foreground">
            {t("integrations.tokens.create.nameHint")}
          </p>
        )}
      </div>
      {submit.isError && fieldError === undefined && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, submit.error)}
          </AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
        <Button type="submit" disabled={submit.isPending}>
          {t("tokens.create.submit")}
        </Button>
      </DialogFooter>
    </form>
  );
}

/** "Create token": the form, then the value and the Alertmanager configuration once. */
export function IntegrationTokenDialog({
  integrationName,
  create,
  onCreated,
}: {
  integrationName: string;
  create: (name: string) => Promise<IntegrationTokenCreated>;
  onCreated: () => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [created, setCreated] = useState<{ value: string; snippet: string; name: string } | null>(
    null,
  );
  // Each opening and closing starts a new round. A token that arrives in a later round than the form that asked for it
  // (the dialog was closed while the request was on its way) is dropped; the list still shows it to revoke.
  const round = useRef(0);
  const asked = round.current;
  return (
    <>
      <Button
        onClick={() => {
          round.current += 1;
          setOpen(true);
        }}
      >
        {t("tokens.create.start")}
      </Button>
      <Dialog
        open={open}
        disablePointerDismissal={created !== null}
        onOpenChange={(next) => {
          round.current += 1;
          setOpen(next);
          if (!next) {
            setCreated(null);
          }
        }}
      >
        <DialogContent
          closeLabel={t("common.close")}
          className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl"
        >
          {created === null ? (
            <>
              <DialogHeader>
                <DialogTitle>{t("tokens.create.title")}</DialogTitle>
                <DialogDescription>
                  {t("integrations.tokens.create.hint", { name: integrationName })}
                </DialogDescription>
              </DialogHeader>
              <TokenForm
                create={create}
                onCreated={(next) => {
                  if (round.current === asked) {
                    setCreated(next);
                  }
                  onCreated();
                }}
              />
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{t("tokens.created.title")}</DialogTitle>
                <DialogDescription>{t("integrations.tokens.created.hint")}</DialogDescription>
              </DialogHeader>
              <CreatedToken value={created.value} snippet={created.snippet} />
              <DialogFooter>
                <DialogClose render={<Button />}>{t("common.done")}</DialogClose>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
