// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Secrets of an outgoing webhook Destination (C-15.FR-10): named values, encrypted and write-only, that the URL,
// headers and body of both modes read as {{ .Secrets.<name> }}. The list shows each name with "Set", the time of the
// last change and its reference, never a value. "Add secret" takes a name and a value, "Replace" a new value, and
// "Remove" asks first; the value field starts empty, is never filled from the server, and is emptied after saving.
// Changes send the ETag of the list as If-Match; one refused because the Destination changed meanwhile (412) offers to
// read the list again. Without destinations:write the section lists the names only.

import { type QueryClient, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { PlusIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  deleteDestinationSecret,
  getGetDestinationQueryKey,
  getGetSigningSecretQueryKey,
  getListDestinationSecretsQueryKey,
  getListDestinationSecretsUrl,
  setDestinationSecret,
} from "../api/gen/endpoints/destinations/destinations";
import type { DestinationSecretList } from "../api/gen/model";
import { apiFetchTagged, fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { useCan } from "./app-shell";
import { reference } from "./header-editor";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
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

/** A Secret name: a template identifier, as the server checks it. */
const SECRET_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** The id of "Add secret", which takes the focus back when a form or a removed row goes away. */
const ADD_ID = "secret-add";

/** The longest Secret value the server keeps, in bytes. */
const VALUE_MAX = 8192;

function secretsKey(destinationId: string) {
  return [...getListDestinationSecretsQueryKey(destinationId), "tagged"] as const;
}

/**
 * Reads an outgoing webhook Destination again after its Secrets, its Signing secret or its settings changed: each of
 * these gives it a new version, which is the ETag of its Secrets.
 */
export function refreshWebhook(queryClient: QueryClient, destinationId: string): void {
  void queryClient.invalidateQueries({ queryKey: getGetDestinationQueryKey(destinationId) });
  void queryClient.invalidateQueries({
    queryKey: getListDestinationSecretsQueryKey(destinationId),
  });
  void queryClient.invalidateQueries({ queryKey: getGetSigningSecretQueryKey(destinationId) });
}

/** What the form edits: a new Secret, or a new value of a named one. */
type Editing = { mode: "add" } | { mode: "replace"; name: string };

function secretErrorText(t: TFunction, err: unknown): { field?: "name" | "value"; text: string } {
  if (isApiError(err) && (err.status === 400 || err.status === 422)) {
    for (const item of err.errors ?? []) {
      if (item.pointer === "/path/secret_name") {
        return { field: "name", text: t("destinations.secrets.nameInvalid") };
      }
      if (item.pointer === "/value") {
        return {
          field: "value",
          text:
            item.code === "too_long"
              ? t("destinations.secrets.valueTooLong")
              : fieldErrorText(t, item.code),
        };
      }
    }
  }
  return { text: problemText(t, err) };
}

function isOutdated(err: unknown): boolean {
  return isStale(err) || (isApiError(err) && err.status === 428);
}

function SecretForm({
  destinationId,
  editing,
  names,
  etag,
  onDone,
  onReload,
}: {
  destinationId: string;
  editing: Editing;
  names: readonly string[];
  etag: string;
  onDone: () => void;
  onReload: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [name, setName] = useState(editing.mode === "replace" ? editing.name : "");
  const [value, setValue] = useState("");
  const [local, setLocal] = useState<{ field: "name" | "value"; text: string } | null>(null);
  const target = name.trim();
  // The value travels in the request only: the mutation has no variables, so the cache of mutations never holds it.
  const save = useMutation({
    mutationFn: async () => {
      await setDestinationSecret(
        destinationId,
        target,
        { value },
        etag === "" ? undefined : { headers: { "If-Match": etag } },
      );
      setValue("");
      refreshWebhook(queryClient, destinationId);
      onDone();
    },
  });
  const refused = save.isError && !isOutdated(save.error) ? secretErrorText(t, save.error) : null;
  const problem = local ?? refused;
  const base = editing.mode === "add" ? "secret-new" : `secret-${editing.name}`;
  const nameError = problem?.field === "name" ? problem.text : undefined;
  const valueError = problem?.field === "value" ? problem.text : undefined;
  return (
    <form
      noValidate
      className="flex min-w-0 flex-col gap-3 rounded-lg border p-3"
      data-testid="secret-form"
      onSubmit={(e) => {
        e.preventDefault();
        setLocal(null);
        if (editing.mode === "add") {
          if (target === "") {
            setLocal({ field: "name", text: fieldErrorText(t, "required") });
            return;
          }
          if (!SECRET_NAME.test(target)) {
            setLocal({ field: "name", text: t("destinations.secrets.nameInvalid") });
            return;
          }
          if (names.includes(target)) {
            setLocal({ field: "name", text: t("destinations.secrets.nameTaken") });
            return;
          }
        }
        if (value === "") {
          setLocal({ field: "value", text: fieldErrorText(t, "required") });
          return;
        }
        save.mutate();
      }}
    >
      {editing.mode === "add" ? (
        <div className="flex max-w-md min-w-0 flex-col gap-2">
          <Label htmlFor={`${base}-name`}>{t("destinations.secrets.name")}</Label>
          <Input
            id={`${base}-name`}
            className="font-mono"
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            autoFocus
            value={name}
            aria-invalid={nameError !== undefined}
            aria-describedby={
              nameError === undefined ? `${base}-name-hint` : `${base}-name-hint ${base}-name-error`
            }
            onChange={(e) => {
              setName(e.target.value);
              setLocal(null);
            }}
          />
          <p id={`${base}-name-hint`} className="text-sm wrap-anywhere text-muted-foreground">
            {t("destinations.secrets.referenceHint", {
              ref: reference(
                "Secrets",
                target !== "" && SECRET_NAME.test(target) ? target : "<name>",
              ),
            })}
          </p>
          {nameError !== undefined && (
            <p id={`${base}-name-error`} className="text-sm text-destructive">
              {nameError}
            </p>
          )}
        </div>
      ) : (
        <p className="text-sm font-medium">
          {t("destinations.secrets.replaceTitle", { name: editing.name })}
        </p>
      )}
      <div className="flex max-w-md min-w-0 flex-col gap-2">
        <Label htmlFor={`${base}-value`}>{t("destinations.secrets.value")}</Label>
        <Input
          id={`${base}-value`}
          type="password"
          autoComplete="new-password"
          spellCheck={false}
          autoFocus={editing.mode === "replace"}
          maxLength={VALUE_MAX}
          value={value}
          aria-invalid={valueError !== undefined}
          aria-describedby={
            valueError === undefined
              ? `${base}-value-hint`
              : `${base}-value-hint ${base}-value-error`
          }
          onChange={(e) => {
            setValue(e.target.value);
            setLocal(null);
          }}
        />
        <p id={`${base}-value-hint`} className="text-sm text-muted-foreground">
          {t("destinations.secrets.writeOnly")}
        </p>
        {valueError !== undefined && (
          <p id={`${base}-value-error`} className="text-sm text-destructive">
            {valueError}
          </p>
        )}
      </div>
      {save.isError && isOutdated(save.error) && (
        <Alert variant="destructive" data-testid="secrets-stale">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>{t("destinations.secrets.stale")}</span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                save.reset();
                onReload();
              }}
            >
              {t("common.reload")}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {problem !== null && problem.field === undefined && (
        <Alert variant="destructive">
          <AlertDescription className="text-current" data-testid="secrets-error">
            {problem.text}
          </AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={save.isPending}>
          {t("common.save")}
        </Button>
        <Button type="button" variant="outline" onClick={onDone}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}

function RemoveSecret({
  destinationId,
  name,
  etag,
  onReload,
  onRemoved,
}: {
  destinationId: string;
  name: string;
  etag: string;
  onReload: () => void;
  /** Called once the Secret is removed: its row, and the button that opened the dialog, go away. */
  onRemoved: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: async () => {
      await deleteDestinationSecret(
        destinationId,
        name,
        etag === "" ? undefined : { headers: { "If-Match": etag } },
      );
      setOpen(false);
      refreshWebhook(queryClient, destinationId);
      onRemoved();
    },
  });
  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        aria-label={t("destinations.secrets.removeNamed", { name })}
        onClick={() => {
          remove.reset();
          setOpen(true);
        }}
      >
        {t("destinations.secrets.remove")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent closeLabel={t("common.close")}>
          <DialogHeader>
            <DialogTitle className="pr-8 break-words">
              {t("destinations.secrets.removeTitle", { name })}
            </DialogTitle>
            <DialogDescription>
              {t("destinations.secrets.removeHint", { ref: reference("Secrets", name) })}
            </DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive">
              <AlertDescription
                className="flex flex-wrap items-center gap-3 text-current"
                data-testid="secret-remove-error"
              >
                {isOutdated(remove.error) ? (
                  <>
                    <span>{t("destinations.secrets.stale")}</span>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => {
                        setOpen(false);
                        onReload();
                      }}
                    >
                      {t("common.reload")}
                    </Button>
                  </>
                ) : (
                  problemText(t, remove.error)
                )}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button
              type="button"
              variant="destructive"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {t("destinations.secrets.remove")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

export function DestinationSecrets({ destinationId }: { destinationId: string }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const queryClient = useQueryClient();
  const canWrite = useCan("destinations:write");
  const query = useQuery({
    queryKey: secretsKey(destinationId),
    queryFn: ({ signal }) =>
      apiFetchTagged<DestinationSecretList>(getListDestinationSecretsUrl(destinationId), {
        method: "GET",
        signal,
      }),
  });
  const [editing, setEditing] = useState<Editing | null>(null);
  // The button that gets the focus back once the form or the dialog that took it closes.
  const focusTarget = useRef<string | null>(null);
  // A render after the change moves the focus; the counter only asks for that render.
  const [, setFocusRound] = useState(0);
  useEffect(() => {
    if (focusTarget.current !== null) {
      document.getElementById(focusTarget.current)?.focus();
      focusTarget.current = null;
    }
  });
  const returnFocus = (target: string) => {
    focusTarget.current = target;
    setFocusRound((n) => n + 1);
  };
  const close = (target: string) => {
    setEditing(null);
    returnFocus(target);
  };
  const items = query.data?.data.items ?? [];
  const etag = query.data?.etag ?? "";
  const reload = () =>
    void queryClient.invalidateQueries({
      queryKey: getListDestinationSecretsQueryKey(destinationId),
    });
  return (
    <Card data-testid="destination-secrets">
      <CardHeader>
        <CardTitle>
          <h2>{t("destinations.secrets.title")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="text-sm text-muted-foreground">
          {t("destinations.secrets.hint", { ref: reference("Secrets", "token") })}
        </p>
        {query.isError ? (
          <p className="text-sm text-destructive" role="alert">
            {problemText(t, query.error)}
          </p>
        ) : query.isPending ? (
          <p className="text-sm text-muted-foreground" role="status">
            {t("common.loading")}
          </p>
        ) : items.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("destinations.secrets.empty")}</p>
        ) : (
          <ul
            className="flex min-w-0 flex-col divide-y"
            aria-label={t("destinations.secrets.title")}
          >
            {items.map((secret) => (
              <li
                key={secret.name}
                className="flex min-w-0 flex-col gap-2 py-2 first:pt-0 last:pb-0"
                data-testid="secret-row"
              >
                <div className="flex min-w-0 flex-wrap items-center justify-between gap-x-3 gap-y-2">
                  <div className="flex min-w-0 flex-col gap-0.5">
                    <span
                      className="font-mono text-sm font-medium wrap-anywhere"
                      data-testid="secret-name"
                    >
                      {secret.name}
                    </span>
                    <span className="text-sm text-muted-foreground" data-testid="secret-status">
                      {secret.set
                        ? secret.updated_at
                          ? t("secret.setChanged", { time: dateTime(secret.updated_at) })
                          : t("secret.set")
                        : t("secret.notSet")}
                    </span>
                    <code
                      className="text-xs wrap-anywhere text-muted-foreground"
                      data-testid="secret-reference"
                    >
                      {reference("Secrets", secret.name)}
                    </code>
                  </div>
                  {canWrite && (
                    <div className="flex flex-wrap gap-2">
                      <Button
                        id={`secret-replace-${secret.name}`}
                        type="button"
                        variant="outline"
                        size="sm"
                        aria-label={t("destinations.secrets.replaceNamed", { name: secret.name })}
                        onClick={() => setEditing({ mode: "replace", name: secret.name })}
                      >
                        {t("secret.replace")}
                      </Button>
                      <RemoveSecret
                        destinationId={destinationId}
                        name={secret.name}
                        etag={etag}
                        onReload={reload}
                        onRemoved={() => returnFocus(ADD_ID)}
                      />
                    </div>
                  )}
                </div>
                {editing?.mode === "replace" && editing.name === secret.name && (
                  <SecretForm
                    destinationId={destinationId}
                    editing={editing}
                    names={items.map((s) => s.name)}
                    etag={etag}
                    onDone={() => close(`secret-replace-${secret.name}`)}
                    onReload={reload}
                  />
                )}
              </li>
            ))}
          </ul>
        )}
        {canWrite &&
          (editing?.mode === "add" ? (
            <SecretForm
              destinationId={destinationId}
              editing={editing}
              names={items.map((s) => s.name)}
              etag={etag}
              onDone={() => close(ADD_ID)}
              onReload={reload}
            />
          ) : (
            <Button
              id={ADD_ID}
              type="button"
              variant="outline"
              className="w-fit"
              onClick={() => setEditing({ mode: "add" })}
            >
              <PlusIcon aria-hidden="true" />
              {t("destinations.secrets.add")}
            </Button>
          ))}
      </CardContent>
    </Card>
  );
}
