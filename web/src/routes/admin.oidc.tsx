// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The OIDC settings (C-03.FR-5, FR-6, FR-8, FR-19, FR-21, FR-32): every setting, the group mapping editor, the write-only
// client secret with its expiry date, the proxy, the warnings and "Check connection". The form keeps the version it
// was read at and sends it as If-Match, so a save over a newer version is refused instead of overwriting it.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { TriangleAlertIcon } from "lucide-react";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";

import {
  getGetOidcSettingsQueryKey,
  getGetOidcSettingsQueryOptions,
  updateOidcSettings,
  useCheckOidcSettings,
  useGetOidcSettings,
} from "../api/gen/endpoints/oidc/oidc";
import type {
  OidcCheckResult,
  OidcSettings,
  OidcSettingsInput,
  OidcUnmatchedRole,
  OidcWarning,
} from "../api/gen/model";
import { UpdateOidcSettingsBody } from "../api/gen/zod/oidc/oidc.zod";
import { RequirePermission, useCan } from "../components/app-shell";
import { fieldLabel } from "../components/audit-diff";
import {
  GroupMappingEditor,
  type MappingRow,
  mappingRows,
  mappingsOf,
} from "../components/group-mapping-editor";
import {
  CheckField,
  type ProxyField,
  ProxyForm,
  type ProxyValues,
  proxyErrors,
  proxyInput,
  proxyValues,
} from "../components/proxy-form";
import {
  KEEP_SECRET,
  type SecretChange,
  SecretField,
  secretPayload,
} from "../components/secret-field";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import { roleLabel } from "../components/user-create-dialog";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";

export const Route = createFileRoute("/admin/oidc")({
  staticData: { shell: true },
  component: OidcPage,
});

interface OidcValues {
  enabled: boolean;
  display_name: string;
  issuer_url: string;
  client_id: string;
  client_secret: SecretChange;
  client_secret_expires_on: string;
  scopes: string;
  groups_claim: string;
  group_mappings: MappingRow[];
  unmatched_role: OidcUnmatchedRole;
  sync_role: boolean;
  skip_totp_with_idp_mfa: boolean;
  proxy: ProxyValues;
}

function formValues(s: OidcSettings): OidcValues {
  return {
    enabled: s.enabled,
    display_name: s.display_name ?? "",
    issuer_url: s.issuer_url,
    client_id: s.client_id,
    client_secret: KEEP_SECRET,
    client_secret_expires_on: s.client_secret_expires_on ?? "",
    scopes: (s.scopes ?? []).join(" "),
    groups_claim: s.groups_claim,
    group_mappings: mappingRows(s.group_mappings),
    unmatched_role: s.unmatched_role,
    sync_role: s.sync_role,
    skip_totp_with_idp_mfa: s.skip_totp_with_idp_mfa,
    proxy: proxyValues(s.proxy),
  };
}

/** The update the form sends: optional fields the form leaves empty return to their default or are cleared. */
function settingsInput(v: OidcValues): OidcSettingsInput {
  const input: OidcSettingsInput = {
    enabled: v.enabled,
    display_name: v.display_name.trim(),
    issuer_url: v.issuer_url.trim(),
    client_id: v.client_id.trim(),
    client_secret_expires_on: v.client_secret_expires_on === "" ? null : v.client_secret_expires_on,
    scopes: v.scopes.split(/[\s,]+/).filter((s) => s !== ""),
    groups_claim: v.groups_claim.trim(),
    group_mappings: mappingsOf(v.group_mappings),
    unmatched_role: v.unmatched_role,
    sync_role: v.sync_role,
    skip_totp_with_idp_mfa: v.skip_totp_with_idp_mfa,
    proxy: proxyInput(v.proxy),
  };
  const secret = secretPayload(v.client_secret);
  if (typeof secret === "string") {
    input.client_secret = secret;
  }
  return input;
}

/** The pointers of the fields that show their errors themselves. */
const SHOWN = [
  /^\/(display_name|issuer_url|client_id|client_secret|client_secret_expires_on|scopes|groups_claim|unmatched_role)$/,
  /^\/proxy\/(type|address|username|password)$/,
  /^\/group_mappings\/\d+/,
];

/** Field errors by JSON pointer, such as "/issuer_url" or "/proxy/address", with their codes. */
type FieldErrors = Record<string, string>;

/** The checks of the generated schema, and those the server repeats, before the form is sent. */
function validate(input: OidcSettingsInput, values: OidcValues): FieldErrors {
  const errors: FieldErrors = {};
  const parsed = UpdateOidcSettingsBody.safeParse(input);
  if (!parsed.success) {
    for (const issue of parsed.error.issues) {
      errors[`/${issue.path.join("/")}`] =
        issue.code === "too_small" ? "required" : "invalid_format";
    }
  }
  if (values.client_secret.mode === "replace" && values.client_secret.value === "") {
    errors["/client_secret"] = "required";
  }
  for (const [field, code] of Object.entries(proxyErrors(values.proxy))) {
    errors[`/proxy/${field}`] = code;
  }
  return errors;
}

function WarningAlert({ children, testId }: { children: ReactNode; testId: string }) {
  return (
    <div
      className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm"
      data-testid={testId}
    >
      <TriangleAlertIcon
        aria-hidden="true"
        className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
      />
      <p>{children}</p>
    </div>
  );
}

function warningText(t: TFunction, w: OidcWarning, date: (d: string) => string): string | null {
  switch (w.kind) {
    case "nobody_can_sign_in":
    case "groups_claim_missing":
      return t("oidc.warnings.nobodyCanSignIn");
    case "secret_expiring":
      return w.expires_on ? t("oidc.warnings.secretExpiring", { date: date(w.expires_on) }) : null;
    case "last_admin_kept":
      return w.user && w.role
        ? t("oidc.warnings.lastAdminKept", { user: w.user.name, role: roleLabel(t, w.role) })
        : null;
    default:
      return null;
  }
}

/** The warnings of the settings, each text once: two kinds share "Nobody will be able to sign in through OIDC." */
function Warnings({ warnings }: { warnings: readonly OidcWarning[] }) {
  const { t } = useTranslation();
  const { date } = useTimeFormat();
  const texts = [
    ...new Set(
      warnings.map((w) => warningText(t, w, date)).filter((text): text is string => text !== null),
    ),
  ];
  return (
    <div role="status" aria-live="polite" className="flex flex-col gap-2 empty:hidden">
      {texts.map((text) => (
        <WarningAlert key={text} testId="oidc-warning">
          {text}
        </WarningAlert>
      ))}
    </div>
  );
}

function checkText(t: TFunction, result: OidcCheckResult): string {
  const proxy = result.via === "proxy";
  if (result.ok) {
    return proxy
      ? t("oidc.check.okProxy", { ms: result.latency_ms ?? 0 })
      : t("oidc.check.okDirect", { ms: result.latency_ms ?? 0 });
  }
  const error = result.error ?? t("oidc.check.unknownError");
  return proxy ? t("oidc.check.failedProxy", { error }) : t("oidc.check.failedDirect", { error });
}

function ConnectionCheck({ dirty }: { dirty: boolean }) {
  const { t } = useTranslation();
  const check = useCheckOidcSettings();
  const result = check.data;
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("oidc.check.title")}</h2>
        </CardTitle>
        <CardDescription>{t("oidc.check.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="outline" disabled={check.isPending} onClick={() => check.mutate()}>
            {check.isPending ? t("oidc.check.running") : t("oidc.check.start")}
          </Button>
          {dirty && (
            <span className="text-sm text-muted-foreground">{t("oidc.check.unsaved")}</span>
          )}
        </div>
        <div className="flex flex-col gap-2 empty:hidden">
          {check.isError && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {problemText(t, check.error)}
              </AlertDescription>
            </Alert>
          )}
          {result !== undefined && (
            <>
              <Alert
                variant={result.ok ? "default" : "destructive"}
                data-testid="oidc-check-result"
              >
                <AlertDescription className="text-current">{checkText(t, result)}</AlertDescription>
              </Alert>
              {result.ok && (
                <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
                  {(
                    [
                      [t("oidc.check.issuer"), result.issuer],
                      [t("oidc.check.authorizationEndpoint"), result.authorization_endpoint],
                      [t("oidc.check.tokenEndpoint"), result.token_endpoint],
                      [t("oidc.check.jwksUri"), result.jwks_uri],
                    ] as const
                  ).map(([label, value]) => (
                    <div key={label} className="contents">
                      <dt className="text-muted-foreground">{label}</dt>
                      <dd className="min-w-0 font-mono text-xs break-all">{value ?? "—"}</dd>
                    </div>
                  ))}
                </dl>
              )}
              <Warnings warnings={result.warnings ?? []} />
            </>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function OidcForm({ settings }: { settings: OidcSettings }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const canWrite = useCan("oidc:write");
  // The version the form was read at: a newer one on the server never replaces what the Admin sees unasked.
  const [base, setBase] = useState(settings);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [saved, setSaved] = useState(false);
  const form = useForm<OidcValues>({ defaultValues: formValues(settings) });
  const { isDirty } = form.formState;
  const formElement = useRef<HTMLFormElement>(null);
  const [focusRequest, setFocusRequest] = useState(0);
  // A field's errors go away once it is edited.
  useEffect(() => {
    return form.subscribe({
      formState: { values: true },
      callback: ({ name }) => {
        if (name === undefined) {
          return;
        }
        const prefix = `/${name.split(".")[0]}`;
        setErrors((current) => {
          const kept = Object.entries(current).filter(([pointer]) => !pointer.startsWith(prefix));
          return kept.length === Object.keys(current).length ? current : Object.fromEntries(kept);
        });
      },
    });
  }, [form]);
  // After a refused save, the focus goes to the first marked field.
  useEffect(() => {
    if (focusRequest > 0) {
      formElement.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
    }
  }, [focusRequest]);
  const save = useMutation({
    mutationFn: (input: OidcSettingsInput) =>
      updateOidcSettings(input, { headers: { "If-Match": base.etag } }),
    // A read still on its way must not bring back the version this save replaces.
    onMutate: () => queryClient.cancelQueries({ queryKey: getGetOidcSettingsQueryKey() }),
    onSuccess: (updated) => {
      queryClient.setQueryData(getGetOidcSettingsQueryKey(), updated);
      setBase(updated);
      form.reset(formValues(updated));
      setSaved(true);
    },
    onError: (err) => {
      if (isApiError(err) && err.errors?.length) {
        setErrors(Object.fromEntries(err.errors.map((e) => [e.pointer, e.code])));
        setFocusRequest((n) => n + 1);
      }
    },
  });
  const reload = () => {
    save.reset();
    setErrors({});
    setSaved(false);
    void queryClient
      .fetchQuery({ ...getGetOidcSettingsQueryOptions(), staleTime: 0 })
      .then((fresh) => {
        setBase(fresh);
        form.reset(formValues(fresh));
      });
  };
  const newer = settings.etag !== base.etag && !save.isPending;
  const err = (pointer: string) =>
    errors[pointer] === undefined ? undefined : fieldErrorText(t, errors[pointer]);
  const proxyFieldErrors: Partial<Record<ProxyField, string>> = {};
  for (const field of ["type", "address", "username", "password"] as const) {
    const text = err(`/proxy/${field}`);
    if (text !== undefined) {
      proxyFieldErrors[field] = text;
    }
  }
  const mappingErrors = new Map<number, string>();
  for (const [pointer, code] of Object.entries(errors)) {
    const match = /^\/group_mappings\/(\d+)/.exec(pointer);
    if (match?.[1] !== undefined) {
      mappingErrors.set(Number(match[1]), fieldErrorText(t, code));
    }
  }
  const text = (
    name: "display_name" | "issuer_url" | "client_id" | "groups_claim" | "scopes",
    label: string,
    hint?: string,
  ) => {
    const id = `oidc-${name}`;
    const error = err(`/${name}`);
    const describedBy =
      [hint === undefined ? null : `${id}-hint`, error === undefined ? null : `${id}-error`]
        .filter(Boolean)
        .join(" ") || undefined;
    return (
      <div className="flex flex-col gap-2">
        <Label htmlFor={id}>{label}</Label>
        <Input
          id={id}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={error !== undefined}
          aria-describedby={describedBy}
          {...form.register(name)}
        />
        {hint !== undefined && (
          <p id={`${id}-hint`} className="text-sm text-muted-foreground">
            {hint}
          </p>
        )}
        {error !== undefined && (
          <p id={`${id}-error`} className="text-sm text-destructive">
            {error}
          </p>
        )}
      </div>
    );
  };
  // Errors on fields the form does not show apart, such as the whole mapping, are listed with the save.
  const unshown = Object.entries(errors).filter(
    ([pointer]) => !SHOWN.some((re) => re.test(pointer)),
  );
  const showStale = newer || isStale(save.error);
  const otherError =
    save.isError && !isStale(save.error) && !(isApiError(save.error) && save.error.errors?.length);
  return (
    <>
      <form
        ref={formElement}
        noValidate
        className="flex flex-col gap-6"
        onSubmit={form.handleSubmit((values) => {
          setSaved(false);
          const input = settingsInput(values);
          const found = validate(input, values);
          setErrors(found);
          setFocusRequest((n) => n + 1);
          if (Object.keys(found).length === 0) {
            save.mutate(input);
          }
        })}
      >
        <fieldset disabled={!canWrite} className="flex min-w-0 flex-col gap-6">
          <Card>
            <CardHeader>
              <CardTitle>
                <h2>{t("oidc.provider.title")}</h2>
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-4">
              <Controller
                control={form.control}
                name="enabled"
                render={({ field }) => (
                  <CheckField
                    id="oidc-enabled"
                    label={t("oidc.fields.enabled")}
                    hint={t("oidc.fields.enabledHint")}
                    checked={field.value}
                    disabled={!canWrite}
                    onChange={field.onChange}
                  />
                )}
              />
              <div className="grid gap-4 md:grid-cols-2">
                {text("issuer_url", t("oidc.fields.issuerUrl"))}
                {text(
                  "display_name",
                  t("oidc.fields.displayName"),
                  t("oidc.fields.displayNameHint"),
                )}
                {text("client_id", t("oidc.fields.clientId"))}
                <Controller
                  control={form.control}
                  name="client_secret"
                  render={({ field }) => (
                    <SecretField
                      id="oidc-client-secret"
                      label={t("oidc.fields.clientSecret")}
                      status={base.client_secret_status}
                      value={field.value}
                      disabled={!canWrite}
                      error={err("/client_secret")}
                      onChange={field.onChange}
                    />
                  )}
                />
                <div className="flex flex-col gap-2">
                  <Label htmlFor="oidc-secret-expires-on">{t("oidc.fields.secretExpiresOn")}</Label>
                  <Input
                    id="oidc-secret-expires-on"
                    type="date"
                    className="w-fit"
                    aria-describedby={
                      err("/client_secret_expires_on") === undefined
                        ? "oidc-secret-expires-on-hint"
                        : "oidc-secret-expires-on-hint oidc-secret-expires-on-error"
                    }
                    aria-invalid={err("/client_secret_expires_on") !== undefined}
                    {...form.register("client_secret_expires_on")}
                  />
                  <p id="oidc-secret-expires-on-hint" className="text-sm text-muted-foreground">
                    {t("oidc.fields.secretExpiresOnHint")}
                  </p>
                  {err("/client_secret_expires_on") !== undefined && (
                    <p id="oidc-secret-expires-on-error" className="text-sm text-destructive">
                      {err("/client_secret_expires_on")}
                    </p>
                  )}
                </div>
                {text("scopes", t("oidc.fields.scopes"), t("oidc.fields.scopesHint"))}
              </div>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>
                <h2>{t("oidc.roles.title")}</h2>
              </CardTitle>
              <CardDescription>{t("oidc.roles.hint")}</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-5">
              <div className="md:max-w-sm">
                {text("groups_claim", t("oidc.fields.groupsClaim"))}
              </div>
              <Controller
                control={form.control}
                name="group_mappings"
                render={({ field }) => (
                  <GroupMappingEditor
                    value={field.value}
                    disabled={!canWrite}
                    errors={mappingErrors}
                    onChange={field.onChange}
                  />
                )}
              />
              <div className="flex flex-col gap-2">
                <Label htmlFor="oidc-unmatched-role">{t("oidc.fields.unmatchedRole")}</Label>
                <NativeSelect
                  id="oidc-unmatched-role"
                  className="w-full max-w-sm"
                  aria-invalid={err("/unmatched_role") !== undefined}
                  aria-describedby={
                    err("/unmatched_role") === undefined
                      ? "oidc-unmatched-role-hint"
                      : "oidc-unmatched-role-hint oidc-unmatched-role-error"
                  }
                  {...form.register("unmatched_role")}
                >
                  {(["none", "viewer", "responder"] as const).map((role) => (
                    <NativeSelectOption key={role} value={role}>
                      {roleLabel(t, role)}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
                <p id="oidc-unmatched-role-hint" className="text-sm text-muted-foreground">
                  {t("oidc.fields.unmatchedRoleHint")}
                </p>
                {err("/unmatched_role") !== undefined && (
                  <p id="oidc-unmatched-role-error" className="text-sm text-destructive">
                    {err("/unmatched_role")}
                  </p>
                )}
              </div>
              <Controller
                control={form.control}
                name="sync_role"
                render={({ field }) => (
                  <CheckField
                    id="oidc-sync-role"
                    label={t("oidc.fields.syncRole")}
                    hint={t("oidc.fields.syncRoleHint")}
                    checked={field.value}
                    disabled={!canWrite}
                    onChange={field.onChange}
                  />
                )}
              />
              <Controller
                control={form.control}
                name="skip_totp_with_idp_mfa"
                render={({ field }) => (
                  <CheckField
                    id="oidc-skip-totp"
                    label={t("oidc.fields.skipTotp")}
                    hint={t("oidc.fields.skipTotpHint")}
                    checked={field.value}
                    disabled={!canWrite}
                    onChange={field.onChange}
                  />
                )}
              />
            </CardContent>
          </Card>
          <Card>
            <CardContent>
              <Controller
                control={form.control}
                name="proxy"
                render={({ field }) => (
                  <ProxyForm
                    idPrefix="oidc-proxy"
                    value={field.value}
                    passwordStatus={base.proxy.password_status}
                    errors={proxyFieldErrors}
                    disabled={!canWrite}
                    onChange={field.onChange}
                  />
                )}
              />
            </CardContent>
          </Card>
        </fieldset>
        {canWrite && (
          <div className="flex flex-col gap-3">
            {showStale && (
              <Alert variant="destructive">
                <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
                  <span>{t("errors.stale")}</span>
                  <Button variant="outline" size="sm" onClick={reload}>
                    {t("common.reload")}
                  </Button>
                </AlertDescription>
              </Alert>
            )}
            {otherError && (
              <Alert variant="destructive">
                <AlertDescription className="text-current">
                  {problemText(t, save.error)}
                </AlertDescription>
              </Alert>
            )}
            {Object.keys(errors).length > 0 && (
              <div className="text-sm text-destructive" role="alert">
                <p>{t("oidc.fixErrors")}</p>
                {unshown.length > 0 && (
                  <ul className="mt-1 list-disc pl-5">
                    {unshown.map(([pointer, code]) => (
                      <li key={pointer}>
                        {fieldLabel(t, "oidc_settings", pointer)}: {fieldErrorText(t, code)}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            )}
            <div className="flex items-center gap-3">
              <Button type="submit" disabled={save.isPending}>
                {t("common.save")}
              </Button>
              <span
                role="status"
                className="text-sm text-muted-foreground"
                data-testid="oidc-status"
              >
                {saved && !isDirty ? t("common.saved") : ""}
              </span>
            </div>
          </div>
        )}
      </form>
      {canWrite && <ConnectionCheck dirty={isDirty} />}
    </>
  );
}

function OidcView() {
  const { t } = useTranslation();
  const query = useGetOidcSettings();
  const settings = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-1">
        <h1 className="text-2xl font-semibold tracking-tight">{t("oidc.title")}</h1>
        <p className="text-muted-foreground">{t("oidc.hint")}</p>
      </div>
      {settings === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <>
          <Warnings warnings={settings.warnings} />
          <OidcForm settings={settings} />
        </>
      )}
    </div>
  );
}

function OidcPage() {
  return (
    <RequirePermission permission="oidc:read">
      <OidcView />
    </RequirePermission>
  );
}
