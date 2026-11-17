// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Contact for calls and SMS" of the profile (C-30.FR-3, FR-9): one block per Notification gateway, in the order of the
// list of gateways. A block has the phone field with "Send code"; then, once a contact waits for its code, "Code sent
// through the Notification gateway to {contact}." with the code field, "Confirm" and "Another number"; then the
// confirmed contact with "calls and SMS", "confirmed" and "Remove". While a new contact waits for its code, the
// confirmed one that it replaces stays listed, since it is still the one called (S-167). A wrong code shows the
// attempts left, too many code requests the time to wait. Without a gateway the section shows GatewayUnavailable.
// A contact is set from the person's own web session only; an Admin sees and removes it on the User's page.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { useListConnections } from "../api/gen/endpoints/connections/connections";
import {
  confirmMyGatewayContact,
  createMyGatewayContact,
  deleteMyGatewayContact,
  getListMyGatewayContactsQueryKey,
  sendMyGatewayContactCode,
  useListMyGatewayContacts,
} from "../api/gen/endpoints/notification-gateways/notification-gateways";
import type { EntityRef, GatewayContact } from "../api/gen/model";
import { fieldErrorText, isApiError, problemText, useSession } from "../lib/api";
import { useCan } from "./app-shell";
import { DocsLink } from "./docs-link";
import { GatewayUnavailable } from "./gateway-unavailable";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
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

/** The longest contact, in characters, that the server takes. */
const CONTACT_MAX = 200;

/** Where a refusal shows: under the phone field, under the code field, or for the whole block. */
type Place = "contact" | "code" | "block";

function hasFieldCode(err: unknown, code: string): boolean {
  return isApiError(err) && (err.errors ?? []).some((e) => e.code === code);
}

/**
 * The text of a refused request and where it shows: a contact the server refuses and a gateway that is no more belong
 * to the phone field, as does too many code requests (the time to wait, from Retry-After); a wrong or void code to the
 * code; a gateway that refused the code, a busy interactive path and anything else to the block.
 */
export function contactProblem(t: TFunction, err: unknown): { at: Place; text: string } {
  if (isApiError(err)) {
    if (err.status === 429 && err.code === "contact_code_requests_exceeded") {
      return {
        at: "contact",
        text: t("gatewayContact.errors.tooManyCodes", {
          count: Math.max(1, Math.ceil((err.retry_after_seconds ?? 60) / 60)),
        }),
      };
    }
    if (err.status === 502) {
      return { at: "block", text: t("gatewayContact.errors.refused") };
    }
    if (err.status === 503) {
      return { at: "block", text: t("accountLinks.errors.busy") };
    }
    if (err.status === 422) {
      const errors = err.errors ?? [];
      if (errors.some((e) => e.code === "unknown_id")) {
        return { at: "block", text: t("gatewayContact.errors.unknownGateway") };
      }
      if (errors.length > 0) {
        const at = errors.every((e) => e.pointer === "/code") ? "code" : "contact";
        return { at, text: errors.map((e) => fieldErrorText(t, e.code)).join(" ") };
      }
    }
  }
  return { at: "block", text: problemText(t, err) };
}

/** Asks before a contact is removed; calls and SMS no longer reach the person through that gateway. */
export function RemoveContactDialog({
  contact,
  description,
  pending,
  error,
  onOpenChange,
  onConfirm,
}: {
  contact: GatewayContact | null;
  description: (contact: GatewayContact) => string;
  pending: boolean;
  error: unknown;
  onOpenChange: (open: boolean) => void;
  onConfirm: (contact: GatewayContact) => void;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={contact !== null} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{t("gatewayContact.remove.action")}</DialogTitle>
          <DialogDescription className="break-words">
            {contact === null ? "" : description(contact)}
          </DialogDescription>
        </DialogHeader>
        {error !== null && error !== undefined && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">{problemText(t, error)}</AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
          <Button
            variant="destructive"
            disabled={pending}
            onClick={() => {
              if (contact !== null) {
                onConfirm(contact);
              }
            }}
          >
            {t("gatewayContact.remove.action")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function FieldError({ id, text }: { id: string; text: string }) {
  return (
    <p id={id} className="text-sm wrap-anywhere text-destructive" role="alert">
      {text}
    </p>
  );
}

/** The block of one Notification gateway: its confirmed contact, and the one that waits for its code. */
function GatewayBlock({
  gateway,
  contacts,
}: {
  gateway: EntityRef;
  contacts: readonly GatewayContact[];
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const confirmed = contacts.find((c) => c.confirmed);
  const waiting = contacts.find((c) => !c.confirmed);
  const [phone, setPhone] = useState("");
  const [phoneCheck, setPhoneCheck] = useState<string | null>(null);
  const [code, setCode] = useState("");
  // Another number: the phone field shows although a contact waits or is confirmed.
  const [another, setAnother] = useState(false);
  // Whether the code of the waiting contact was sent in this page, and the attempts left that the server last gave.
  const [sent, setSent] = useState(false);
  const [attempts, setAttempts] = useState<number | null>(null);
  const [removing, setRemoving] = useState<GatewayContact | null>(null);
  const id = `gateway-${gateway.id}`;
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: getListMyGatewayContactsQueryKey() });

  const create = useMutation({
    mutationFn: () => createMyGatewayContact({ connection_id: gateway.id, contact: phone.trim() }),
    onSuccess: () => {
      setSent(true);
      setAnother(false);
      setAttempts(null);
      setCode("");
      setPhone("");
      setPhoneCheck(null);
      resend.reset();
      confirm.reset();
      void refresh();
    },
    // The contact stays, unconfirmed, when the gateway refused or the path was busy.
    onError: (err) => {
      if (isApiError(err) && (err.status === 502 || err.status === 503)) {
        setAnother(false);
        void refresh();
      }
    },
  });
  const resend = useMutation({
    mutationFn: (contact: GatewayContact) => sendMyGatewayContactCode(contact.id),
    onSuccess: () => {
      setSent(true);
      setAttempts(null);
      setCode("");
      create.reset();
    },
  });
  const confirm = useMutation({
    mutationFn: (contact: GatewayContact) =>
      confirmMyGatewayContact(contact.id, { code: code.trim() }),
    onSuccess: () => {
      setCode("");
      setAttempts(null);
      setSent(false);
      create.reset();
      resend.reset();
      void refresh();
    },
    onError: (err) => {
      if (hasFieldCode(err, "contact_code_invalid")) {
        setAttempts(isApiError(err) ? Math.max(0, err.attempts_remaining ?? 0) : 0);
        setCode("");
      }
    },
  });
  const remove = useMutation({
    mutationFn: (contact: GatewayContact) => deleteMyGatewayContact(contact.id),
    onSuccess: () => {
      setRemoving(null);
      setSent(false);
      setAnother(false);
      create.reset();
      resend.reset();
      confirm.reset();
      void refresh();
    },
  });

  const showCodeStep = waiting !== undefined && !another;
  const showPhone = !showCodeStep && (confirmed === undefined || another);
  const sendProblem = create.error === null ? null : contactProblem(t, create.error);
  const phoneError =
    phoneCheck !== null
      ? fieldErrorText(t, phoneCheck)
      : sendProblem?.at === "contact"
        ? sendProblem.text
        : null;
  const codeProblem =
    confirm.error !== null
      ? contactProblem(t, confirm.error)
      : resend.error !== null
        ? contactProblem(t, resend.error)
        : null;
  const blockProblem =
    sendProblem?.at === "block" ? sendProblem : codeProblem?.at === "block" ? codeProblem : null;
  const wrong = hasFieldCode(confirm.error, "contact_code_invalid");
  const spent = attempts !== null && attempts <= 0;

  const sendCode = () => {
    const trimmed = phone.trim();
    const check =
      trimmed === "" ? "required" : Array.from(trimmed).length > CONTACT_MAX ? "too_long" : null;
    setPhoneCheck(check);
    if (check !== null) {
      return;
    }
    create.reset();
    create.mutate();
  };

  return (
    <section
      className="flex min-w-0 flex-col gap-3"
      aria-labelledby={`${id}-name`}
      data-testid="gateway-block"
      data-gateway={gateway.id}
    >
      <h3 id={`${id}-name`} className="text-sm font-medium wrap-anywhere">
        {gateway.name}
      </h3>
      {confirmed !== undefined && (
        <div
          className="flex flex-wrap items-center gap-3 rounded-lg border px-3.5 py-3 text-sm"
          data-testid="gateway-contact-confirmed"
        >
          <div className="flex min-w-0 flex-1 basis-56 flex-col">
            <span className="font-medium wrap-anywhere" data-testid="gateway-contact-summary">
              {confirmed.contact}
              <span className="font-normal text-muted-foreground">
                {" · "}
                {t("gatewayContact.callsAndSms")}
                {" · "}
              </span>
              <span className="rounded-full bg-emerald-100 px-2 py-px text-xs font-semibold text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">
                {t("gatewayContact.confirmed")}
              </span>
            </span>
            {waiting !== undefined && (
              <span className="text-muted-foreground wrap-anywhere" data-testid="gateway-replaced">
                {t("gatewayContact.replacedBy", { contact: waiting.contact })}
              </span>
            )}
          </div>
          {waiting === undefined && !another && (
            <Button variant="outline" size="sm" onClick={() => setAnother(true)}>
              {t("gatewayContact.anotherNumber")}
            </Button>
          )}
          <Button
            variant="outline"
            size="sm"
            aria-label={t("gatewayContact.remove.label", { contact: confirmed.contact })}
            onClick={() => {
              remove.reset();
              setRemoving(confirmed);
            }}
          >
            {t("gatewayContact.remove.action")}
          </Button>
        </div>
      )}
      {showPhone && (
        <form
          noValidate
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            sendCode();
          }}
        >
          <div className="flex min-w-0 flex-1 basis-60 flex-col gap-2">
            <Label htmlFor={`${id}-phone`}>{t("gatewayContact.phone")}</Label>
            <Input
              id={`${id}-phone`}
              type="tel"
              autoComplete="tel"
              spellCheck={false}
              value={phone}
              aria-invalid={phoneError !== null}
              aria-describedby={phoneError === null ? undefined : `${id}-phone-error`}
              onChange={(e) => setPhone(e.target.value)}
            />
          </div>
          <Button type="submit" disabled={create.isPending}>
            {t("gatewayContact.sendCode")}
          </Button>
          {(confirmed !== undefined || waiting !== undefined) && (
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                setAnother(false);
                setPhone("");
                setPhoneCheck(null);
                create.reset();
                resend.reset();
              }}
            >
              {t("common.cancel")}
            </Button>
          )}
          {phoneError !== null && <FieldError id={`${id}-phone-error`} text={phoneError} />}
        </form>
      )}
      {showCodeStep && (
        <form
          noValidate
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!spent && code.trim() !== "") {
              confirm.reset();
              confirm.mutate(waiting);
            }
          }}
        >
          <p className="text-sm wrap-anywhere" data-testid="gateway-code-sent">
            {sent
              ? t("gatewayContact.codeSent", { contact: waiting.contact })
              : t("gatewayContact.codeWaiting", { contact: waiting.contact })}
          </p>
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex min-w-0 flex-1 basis-40 flex-col gap-2 sm:max-w-52">
              <Label htmlFor={`${id}-code`}>{t("gatewayContact.code")}</Label>
              <Input
                id={`${id}-code`}
                inputMode="numeric"
                autoComplete="one-time-code"
                spellCheck={false}
                value={code}
                aria-invalid={codeProblem?.at === "code" || wrong}
                aria-describedby={`${id}-code-error`}
                onChange={(e) => setCode(e.target.value)}
              />
            </div>
            <Button type="submit" disabled={confirm.isPending || spent}>
              {t("gatewayContact.confirm")}
            </Button>
            <Button type="button" variant="outline" onClick={() => setAnother(true)}>
              {t("gatewayContact.anotherNumber")}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={resend.isPending}
              onClick={() => {
                resend.reset();
                confirm.reset();
                resend.mutate(waiting);
              }}
            >
              {t("gatewayContact.newCode")}
            </Button>
          </div>
          <div id={`${id}-code-error`} className="flex flex-col gap-1 empty:hidden">
            {wrong && (
              <FieldError
                id={`${id}-code-wrong`}
                text={
                  attempts !== null && attempts <= 0
                    ? t("gatewayContact.errors.noAttempts")
                    : t("gatewayContact.errors.codeInvalid", { count: attempts ?? 0 })
                }
              />
            )}
            {!wrong && codeProblem !== null && codeProblem.at !== "block" && (
              <FieldError id={`${id}-code-problem`} text={codeProblem.text} />
            )}
          </div>
        </form>
      )}
      {blockProblem !== null && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">{blockProblem.text}</AlertDescription>
        </Alert>
      )}
      <RemoveContactDialog
        contact={removing}
        description={(c) =>
          t("gatewayContact.remove.confirm", { contact: c.contact, gateway: gateway.name })
        }
        pending={remove.isPending}
        error={remove.error}
        onOpenChange={(open) => {
          if (!open) {
            setRemoving(null);
          }
        }}
        onConfirm={(c) => remove.mutate(c)}
      />
    </section>
  );
}

export function GatewayContactSection() {
  const { t } = useTranslation();
  const canRead = useCan("connections:read");
  const session = useSession();
  const contacts = useListMyGatewayContacts();
  const gateways = useListConnections(
    { role: "gateway", limit: 500 },
    { query: { enabled: canRead } },
  );
  const items = contacts.data?.items ?? [];
  // The gateways of the list, then any gateway only a contact names, for a user who cannot read Connections.
  const blocks: EntityRef[] = (gateways.data?.items ?? []).map((c) => ({ id: c.id, name: c.name }));
  for (const c of items) {
    if (!blocks.some((b) => b.id === c.connection.id)) {
      blocks.push(c.connection);
    }
  }
  const settled = (!canRead || !gateways.isPending) && !contacts.isPending;
  // A refetch that fails keeps the blocks the last read made; only a list that was never read is an error.
  const failed =
    contacts.data === undefined && contacts.error !== null
      ? contacts.error
      : gateways.data === undefined && canRead && gateways.error !== null
        ? gateways.error
        : null;
  return (
    <Card className="lg:col-span-2" data-testid="gateway-contact-section">
      <CardHeader>
        <CardTitle>
          <h2>{t("gatewayContact.title")}</h2>
        </CardTitle>
        <CardDescription>{t("gatewayContact.hint")}</CardDescription>
        <DocsLink
          to="field.gateway_contact"
          kind="learn_more"
          about={t("docs.fields.gatewayContact")}
        />
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        {failed !== null && failed !== undefined ? (
          <p className="text-sm text-destructive" role="alert" data-testid="gateway-contact-error">
            {problemText(t, failed)}
          </p>
        ) : !settled ? (
          <p className="text-sm text-muted-foreground" role="status">
            {t("common.loading")}
          </p>
        ) : blocks.length === 0 ? (
          <GatewayUnavailable />
        ) : (
          <>
            {blocks.map((gateway) => (
              <GatewayBlock
                key={gateway.id}
                gateway={gateway}
                contacts={items.filter((c) => c.connection.id === gateway.id)}
              />
            ))}
            {items.some((c) => c.confirmed) && (
              <p className="text-sm text-muted-foreground" data-testid="gateway-contact-note">
                {t("gatewayContact.note", { name: session?.user.name ?? "" })}
              </p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
