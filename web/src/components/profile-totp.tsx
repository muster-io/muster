// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// TOTP (C-03.FR-10): the enrolment with its QR code, secret and first code, the recovery codes shown once, and the
// TOTP section of the profile with regenerating the codes and removing TOTP. The enrolment page reuses the enrolment.

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { CheckIcon, CopyIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  getGetMyTotpQueryKey,
  useBeginTotpEnrolment,
  useConfirmTotpEnrolment,
  useGetMyTotp,
  useRegenerateTotpRecoveryCodes,
  useRemoveTotp,
} from "../api/gen/endpoints/profile/profile";
import type { TotpRemoval } from "../api/gen/model";
import { SESSION_QUERY_KEY, isApiError, problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { QrCode } from "./qr-code";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

const codeSchema = z.object({ code: z.string().trim().min(1, "required") });
type CodeValues = z.infer<typeof codeSchema>;

/** The base32 secret in groups of four, easier to type by hand. */
export function groupSecret(secret: string): string {
  return secret.replace(/(.{4})/g, "$1 ").trim();
}

function ErrorAlert({ text }: { text: string }) {
  return (
    <Alert variant="destructive">
      <AlertDescription className="text-current">{text}</AlertDescription>
    </Alert>
  );
}

/** A one-field form for a TOTP code. */
function CodeForm({
  id,
  label,
  submitLabel,
  pending,
  onSubmit,
  onCancel,
}: {
  id: string;
  label: string;
  submitLabel: string;
  pending: boolean;
  onSubmit: (code: string) => void;
  onCancel?: () => void;
}) {
  const { t } = useTranslation();
  const form = useForm<CodeValues>({
    resolver: zodResolver(codeSchema),
    defaultValues: { code: "" },
  });
  const error = form.formState.errors.code;
  return (
    <form
      noValidate
      className="flex flex-col gap-3"
      onSubmit={form.handleSubmit(({ code }) => {
        onSubmit(code.replace(/\s/g, ""));
        form.reset({ code: "" });
      })}
    >
      <div className="flex flex-col gap-2">
        <Label htmlFor={id}>{label}</Label>
        <Input
          id={id}
          className="max-w-48"
          autoComplete="one-time-code"
          inputMode="numeric"
          aria-invalid={error !== undefined}
          aria-describedby={error ? `${id}-error` : undefined}
          {...form.register("code")}
        />
        {error && (
          <p id={`${id}-error`} className="text-sm text-destructive">
            {t("fieldErrors.required")}
          </p>
        )}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={pending}>
          {submitLabel}
        </Button>
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
        )}
      </div>
    </form>
  );
}

/** The recovery codes, shown once, with a copy button and the way on. */
export function RecoveryCodes({
  codes,
  onContinue,
  heading: Heading = "h2",
}: {
  codes: string[];
  onContinue: () => void;
  heading?: "h2" | "h3";
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <section aria-labelledby="recovery-codes-title" className="flex flex-col gap-3">
      <Heading id="recovery-codes-title" className="text-base font-semibold">
        {t("totp.recoveryCodes.title")}
      </Heading>
      <p className="text-sm text-muted-foreground">
        {t("totp.recoveryCodes.hint", { count: codes.length })}
      </p>
      <ol
        className="grid grid-cols-2 gap-x-6 gap-y-1 rounded-lg border bg-muted/40 p-3 font-mono text-sm"
        data-testid="recovery-codes"
      >
        {codes.map((code) => (
          <li key={code}>{code}</li>
        ))}
      </ol>
      <div className="flex flex-wrap gap-2">
        {/* The clipboard needs a secure context, which a plain-http installation is not. */}
        {window.isSecureContext && (
          <Button
            variant="outline"
            onClick={() => {
              navigator.clipboard.writeText(codes.join("\n")).then(
                () => setCopied(true),
                () => setCopied(false),
              );
            }}
          >
            {copied ? <CheckIcon aria-hidden="true" /> : <CopyIcon aria-hidden="true" />}
            {copied ? t("totp.recoveryCodes.copied") : t("totp.recoveryCodes.copy")}
          </Button>
        )}
        <Button onClick={onContinue}>{t("common.continue")}</Button>
      </div>
    </section>
  );
}

/** Begins an enrolment, shows its QR code and secret, and confirms it with a first code. */
export function TotpEnrolment({ onEnrolled }: { onEnrolled: (codes: string[]) => void }) {
  const { t } = useTranslation();
  const begin = useBeginTotpEnrolment();
  const confirm = useConfirmTotpEnrolment({ mutation: { onSuccess: (r) => onEnrolled(r.codes) } });
  const started = useRef(false);
  const { mutate } = begin;
  useEffect(() => {
    if (!started.current) {
      started.current = true;
      mutate();
    }
  }, [mutate]);

  if (begin.isError) {
    const already = isApiError(begin.error) && begin.error.code === "totp_already_enrolled";
    return <ErrorAlert text={already ? t("totp.alreadyEnrolled") : problemText(t, begin.error)} />;
  }
  if (begin.data === undefined) {
    return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>;
  }
  const { secret, otpauth_uri: uri } = begin.data;
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm">{t("totp.enrol.scan")}</p>
      <div className="flex flex-col items-start gap-4 sm:flex-row sm:items-center">
        <QrCode
          value={uri}
          label={t("totp.enrol.qrLabel")}
          className="size-48 shrink-0 rounded-md border"
        />
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-sm text-muted-foreground">{t("totp.enrol.secret")}</span>
          <code className="font-mono text-sm break-all" data-testid="totp-secret">
            {groupSecret(secret)}
          </code>
        </div>
      </div>
      {confirm.isError && (
        <ErrorAlert
          text={
            isApiError(confirm.error) && confirm.error.status === 401
              ? t("totp.wrongCode")
              : problemText(t, confirm.error)
          }
        />
      )}
      <CodeForm
        id="enrol-code"
        label={t("totp.enrol.code")}
        submitLabel={t("totp.enrol.confirm")}
        pending={confirm.isPending}
        onSubmit={(code) => confirm.mutate({ data: { code } })}
      />
    </div>
  );
}

const PROOFS = ["password", "totp_code", "recovery_code"] as const;
type Proof = (typeof PROOFS)[number];

const removalSchema = z.object({ proof: z.string().trim().min(1, "required") });
type RemovalValues = z.infer<typeof removalSchema>;

function RemoveTotp({ hasPassword, onDone }: { hasPassword: boolean; onDone: () => void }) {
  const { t } = useTranslation();
  const [kind, setKind] = useState<Proof>(hasPassword ? "password" : "totp_code");
  const form = useForm<RemovalValues>({
    resolver: zodResolver(removalSchema),
    defaultValues: { proof: "" },
  });
  const remove = useRemoveTotp({
    mutation: { onSuccess: onDone, onError: () => form.reset({ proof: "" }) },
  });
  const error = form.formState.errors.proof;
  const label =
    kind === "password"
      ? t("totp.remove.password")
      : kind === "totp_code"
        ? t("totp.remove.code")
        : t("totp.remove.recoveryCode");
  return (
    <form
      noValidate
      className="flex flex-col gap-3 rounded-lg border p-3"
      onSubmit={form.handleSubmit(({ proof }) => {
        const data: TotpRemoval =
          kind === "totp_code"
            ? { totp_code: proof.replace(/\s/g, "") }
            : kind === "password"
              ? { password: proof }
              : { recovery_code: proof };
        remove.mutate({ data });
      })}
    >
      <p className="text-sm">{t("totp.remove.hint")}</p>
      {remove.isError && (
        <ErrorAlert
          text={
            isApiError(remove.error) && remove.error.status === 401
              ? t("totp.remove.wrongProof")
              : problemText(t, remove.error)
          }
        />
      )}
      <div className="flex flex-col gap-2">
        <Label htmlFor="removal-kind">{t("totp.remove.proofKind")}</Label>
        <NativeSelect
          id="removal-kind"
          value={kind}
          onChange={(e) => setKind(PROOFS.find((p) => p === e.target.value) ?? "totp_code")}
        >
          {hasPassword && (
            <NativeSelectOption value="password">{t("totp.remove.password")}</NativeSelectOption>
          )}
          <NativeSelectOption value="totp_code">{t("totp.remove.code")}</NativeSelectOption>
          <NativeSelectOption value="recovery_code">
            {t("totp.remove.recoveryCode")}
          </NativeSelectOption>
        </NativeSelect>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="removal-proof">{label}</Label>
        <Input
          id="removal-proof"
          className="max-w-72"
          type={kind === "password" ? "password" : "text"}
          autoComplete={kind === "password" ? "current-password" : "one-time-code"}
          aria-invalid={error !== undefined}
          aria-describedby={error ? "removal-proof-error" : undefined}
          {...form.register("proof")}
        />
        {error && (
          <p id="removal-proof-error" className="text-sm text-destructive">
            {t("fieldErrors.required")}
          </p>
        )}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" variant="destructive" disabled={remove.isPending}>
          {t("totp.remove.submit")}
        </Button>
        <Button type="button" variant="outline" onClick={onDone}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}

type Mode = "idle" | "enrol" | "codes" | "regenerate" | "remove";

/** The TOTP section of the profile. */
export function ProfileTotp({ hasPassword }: { hasPassword: boolean }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const queryClient = useQueryClient();
  const status = useGetMyTotp();
  const [mode, setMode] = useState<Mode>("idle");
  const [codes, setCodes] = useState<string[]>([]);
  const regenerate = useRegenerateTotpRecoveryCodes({
    mutation: {
      onSuccess: (r) => {
        setCodes(r.codes);
        setMode("codes");
      },
    },
  });
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: getGetMyTotpQueryKey() });
    void queryClient.invalidateQueries({ queryKey: SESSION_QUERY_KEY });
  };
  const done = () => {
    setMode("idle");
    setCodes([]);
    refresh();
  };

  const totp = status.data;
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("totp.title")}</h2>
        </CardTitle>
        <CardDescription>
          {totp === undefined
            ? t("common.loading")
            : totp.enrolled
              ? t("totp.status.on", {
                  date: totp.enrolled_at ? dateTime(totp.enrolled_at) : "",
                  count: totp.recovery_codes_remaining,
                })
              : t("totp.status.off")}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {mode === "codes" && <RecoveryCodes codes={codes} onContinue={done} heading="h3" />}
        {mode === "enrol" && (
          <>
            <TotpEnrolment
              onEnrolled={(c) => {
                setCodes(c);
                setMode("codes");
              }}
            />
            <div>
              <Button variant="outline" onClick={done}>
                {t("common.cancel")}
              </Button>
            </div>
          </>
        )}
        {mode === "regenerate" && (
          <div className="flex flex-col gap-3 rounded-lg border p-3">
            <p className="text-sm">{t("totp.regenerate.hint")}</p>
            {regenerate.isError && (
              <ErrorAlert
                text={
                  isApiError(regenerate.error) && regenerate.error.status === 401
                    ? t("totp.wrongCode")
                    : problemText(t, regenerate.error)
                }
              />
            )}
            <CodeForm
              id="regenerate-code"
              label={t("totp.enrol.code")}
              submitLabel={t("totp.regenerate.submit")}
              pending={regenerate.isPending}
              onSubmit={(code) => regenerate.mutate({ data: { code } })}
              onCancel={() => {
                regenerate.reset();
                setMode("idle");
              }}
            />
          </div>
        )}
        {mode === "remove" && <RemoveTotp hasPassword={hasPassword} onDone={done} />}
        {mode === "idle" && totp !== undefined && (
          <div className="flex flex-wrap gap-2">
            {totp.enrolled ? (
              <>
                <Button variant="outline" onClick={() => setMode("regenerate")}>
                  {t("totp.regenerate.start")}
                </Button>
                <Button variant="destructive" onClick={() => setMode("remove")}>
                  {t("totp.remove.start")}
                </Button>
              </>
            ) : (
              <Button onClick={() => setMode("enrol")}>{t("totp.enrol.start")}</Button>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
