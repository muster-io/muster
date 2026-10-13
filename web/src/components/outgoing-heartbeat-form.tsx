// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Outgoing heartbeat (C-19.FR-7): the URL that Muster calls while a replica leads, as a write-only
// Secret, and the proxy for it. The update sends every other field of the Organization as it was read, with the
// version the form was read at as If-Match, so a save over a newer version shows a conflict instead of overwriting it.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";

import {
  getGetOrganizationQueryKey,
  getGetOrganizationQueryOptions,
  updateOrganization,
} from "../api/gen/endpoints/organization/organization";
import type { Organization, OrganizationInput, OutgoingHeartbeatResult } from "../api/gen/model";
import { UpdateOrganizationBody } from "../api/gen/zod/organization/organization.zod";
import { fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { fieldLabel } from "./audit-diff";
import {
  type ProxyField,
  ProxyForm,
  type ProxyValues,
  proxyErrors,
  proxyInput,
  proxyValues,
} from "./proxy-form";
import { KEEP_SECRET, type SecretChange, SecretField, secretPayload } from "./secret-field";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent } from "./ui/card";

interface HeartbeatValues {
  url: SecretChange;
  proxy: ProxyValues;
}

function formValues(org: Organization): HeartbeatValues {
  return { url: KEEP_SECRET, proxy: proxyValues(org.outgoing_heartbeat.proxy) };
}

/** The Organization as an update: everything as read, the outgoing heartbeat as the form holds it. */
function organizationInput(org: Organization, v: HeartbeatValues): OrganizationInput {
  const { id: _id, etag: _etag, outgoing_heartbeat: _heartbeat, ...rest } = org;
  const heartbeat: OrganizationInput["outgoing_heartbeat"] = { proxy: proxyInput(v.proxy) };
  const url = secretPayload(v.url);
  if (url !== undefined) {
    heartbeat.url = url === null ? null : url.trim();
  }
  return { ...rest, outgoing_heartbeat: heartbeat };
}

/** The pointers of the fields that show their errors themselves. */
const SHOWN = [
  /^\/outgoing_heartbeat\/url$/,
  /^\/outgoing_heartbeat\/proxy\/(type|address|username|password)$/,
];

/** Field errors by JSON pointer, such as "/outgoing_heartbeat/url", with their codes. */
type FieldErrors = Record<string, string>;

/** The checks of the generated schema, and those the server repeats, before the form is sent. */
function validate(input: OrganizationInput, values: HeartbeatValues): FieldErrors {
  const errors: FieldErrors = {};
  const parsed = UpdateOrganizationBody.safeParse(input);
  if (!parsed.success) {
    for (const issue of parsed.error.issues) {
      errors[`/${issue.path.join("/")}`] =
        issue.code === "too_small" ? "required" : "invalid_format";
    }
  }
  if (values.url.mode === "replace" && values.url.value.trim() === "") {
    errors["/outgoing_heartbeat/url"] = "required";
  }
  for (const [field, code] of Object.entries(proxyErrors(values.proxy))) {
    errors[`/outgoing_heartbeat/proxy/${field}`] = code;
  }
  return errors;
}

export function OutgoingHeartbeatForm({ organization }: { organization: Organization }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  // The version the form was read at: a newer one on the server never replaces what the Admin sees unasked.
  const [base, setBase] = useState(organization);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [saved, setSaved] = useState(false);
  const form = useForm<HeartbeatValues>({ defaultValues: formValues(organization) });
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
        const prefix = `/outgoing_heartbeat/${name.split(".")[0]}`;
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
    mutationFn: (input: OrganizationInput) =>
      updateOrganization(input, { headers: { "If-Match": base.etag } }),
    // A read still on its way (a live hint) must not bring back the version this save replaces.
    onMutate: () => queryClient.cancelQueries({ queryKey: getGetOrganizationQueryKey() }),
    onSuccess: (updated) => {
      queryClient.setQueryData(getGetOrganizationQueryKey(), updated);
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
      .fetchQuery({ ...getGetOrganizationQueryOptions(), staleTime: 0 })
      .then((fresh) => {
        setBase(fresh);
        form.reset(formValues(fresh));
      });
  };
  const newer = organization.etag !== base.etag && !save.isPending;
  const err = (pointer: string) =>
    errors[pointer] === undefined ? undefined : fieldErrorText(t, errors[pointer]);
  const proxyFieldErrors: Partial<Record<ProxyField, string>> = {};
  for (const field of ["type", "address", "username", "password"] as const) {
    const text = err(`/outgoing_heartbeat/proxy/${field}`);
    if (text !== undefined) {
      proxyFieldErrors[field] = text;
    }
  }
  // Errors on fields the form does not show, such as another field of the Organization, are listed with the save.
  const unshown = Object.entries(errors).filter(
    ([pointer]) => !SHOWN.some((re) => re.test(pointer)),
  );
  const showStale = newer || isStale(save.error);
  const otherError =
    save.isError && !isStale(save.error) && !(isApiError(save.error) && save.error.errors?.length);
  return (
    <form
      ref={formElement}
      noValidate
      className="flex flex-col gap-6"
      onSubmit={form.handleSubmit((values) => {
        setSaved(false);
        const input = organizationInput(base, values);
        const found = validate(input, values);
        setErrors(found);
        setFocusRequest((n) => n + 1);
        if (Object.keys(found).length === 0) {
          save.mutate(input);
        }
      })}
    >
      <Card>
        <CardContent className="flex flex-col gap-3">
          <Controller
            control={form.control}
            name="url"
            render={({ field }) => (
              <SecretField
                id="outgoing-heartbeat-url"
                label={t("outgoingHeartbeat.url")}
                status={base.outgoing_heartbeat.url_status}
                value={field.value}
                clearable
                describedBy="outgoing-heartbeat-hint"
                error={err("/outgoing_heartbeat/url")}
                onChange={field.onChange}
              />
            )}
          />
          <p
            id="outgoing-heartbeat-hint"
            className="text-sm text-muted-foreground"
            data-testid="outgoing-heartbeat-hint"
          >
            {t("outgoingHeartbeat.hint")}
          </p>
        </CardContent>
      </Card>
      <Card>
        <CardContent>
          <Controller
            control={form.control}
            name="proxy"
            render={({ field }) => (
              <ProxyForm
                idPrefix="outgoing-heartbeat-proxy"
                value={field.value}
                passwordStatus={base.outgoing_heartbeat.proxy.password_status}
                errors={proxyFieldErrors}
                onChange={field.onChange}
              />
            )}
          />
        </CardContent>
      </Card>
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
            <p>{t("outgoingHeartbeat.fixErrors")}</p>
            {unshown.length > 0 && (
              <ul className="mt-1 list-disc pl-5">
                {unshown.map(([pointer, code]) => (
                  <li key={pointer}>
                    {fieldLabel(t, "organization", pointer)}: {fieldErrorText(t, code)}
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
            data-testid="outgoing-heartbeat-status"
          >
            {saved && !isDirty ? t("common.saved") : ""}
          </span>
        </div>
      </div>
    </form>
  );
}

/** The result of the last outgoing heartbeat, as the System status reads it. */
export function OutgoingHeartbeatLastResult({ result }: { result: OutgoingHeartbeatResult }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  let text: string;
  if (!result.configured) {
    text = t("outgoingHeartbeat.lastResult.notConfigured");
  } else if (!result.last_attempt_at) {
    text = t("outgoingHeartbeat.lastResult.notSent");
  } else if (result.last_ok === true) {
    text = t("outgoingHeartbeat.lastResult.ok", { time: dateTime(result.last_attempt_at) });
  } else if (result.last_error) {
    text = t("outgoingHeartbeat.lastResult.failed", {
      time: dateTime(result.last_attempt_at),
      error: result.last_error,
    });
  } else {
    text = t("outgoingHeartbeat.lastResult.failedNoError", {
      time: dateTime(result.last_attempt_at),
    });
  }
  return (
    <p
      className="text-sm wrap-anywhere"
      aria-live="polite"
      data-testid="outgoing-heartbeat-last-result"
    >
      {text}
    </p>
  );
}
