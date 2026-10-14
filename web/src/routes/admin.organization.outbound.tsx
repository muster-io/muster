// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Outbound addresses (C-20.FR-3): the outbound address policy, standard or strict, its allowed and
// denied entries with an error next to each refused one, and the rules that always apply. Reading needs
// organization:read; saving needs organization:write, with the version the page was read at as If-Match.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useRef, useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import {
  getGetOutboundPolicyQueryKey,
  getGetOutboundPolicyQueryOptions,
  updateOutboundPolicy,
  useGetOutboundPolicy,
} from "../api/gen/endpoints/organization/organization";
import type { OutboundPolicy, OutboundPolicyPolicy } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { NetworkListEditor } from "../components/network-list-editor";
import { PageHeading, SaveBar, errorText } from "../components/organization-form";
import { Card, CardContent, CardHeader, CardTitle } from "../components/ui/card";
import { applyFieldErrors, isApiError, isStale, problemText } from "../lib/api";

export const Route = createFileRoute("/admin/organization/outbound")({
  staticData: { shell: true },
  component: OutboundPage,
});

const entries = z.array(z.string()).superRefine((list, ctx) => {
  const seen = new Set<string>();
  list.forEach((raw, index) => {
    const entry = raw.trim().toLowerCase();
    if (entry === "") {
      ctx.addIssue({ code: "custom", path: [index], message: "required" });
    } else if (seen.has(entry)) {
      ctx.addIssue({ code: "custom", path: [index], message: "duplicate" });
    }
    seen.add(entry);
  });
});
const schema = z.object({
  policy: z.enum(["standard", "strict"]),
  allowed: entries,
  denied: entries,
});
type OutboundValues = z.infer<typeof schema>;

const POLICIES: readonly OutboundPolicyPolicy[] = ["standard", "strict"];

function formValues(p: OutboundPolicy): OutboundValues {
  return { policy: p.policy, allowed: [...p.allowed], denied: [...p.denied] };
}

/** An "Always blocked" rule as the server words it, "<rule>: <networks>", split for the page to name the rule. */
function blockedRule(text: string): { rule: string; networks: string } {
  const at = text.indexOf(": ");
  return at < 0
    ? { rule: text, networks: "" }
    : { rule: text.slice(0, at), networks: text.slice(at + 2) };
}

function OutboundForm({ policy, canWrite }: { policy: OutboundPolicy; canWrite: boolean }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  // The version the form was read at: a newer one on the server never replaces what the Admin sees unasked.
  const [base, setBase] = useState(policy);
  const [saved, setSaved] = useState(false);
  const [unmatched, setUnmatched] = useState<string[]>([]);
  const [focusRequest, setFocusRequest] = useState(0);
  const formRef = useRef<HTMLFormElement>(null);
  const form = useForm<OutboundValues>({
    resolver: zodResolver(schema),
    defaultValues: formValues(policy),
  });
  useEffect(() => {
    if (focusRequest > 0) {
      formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
    }
  }, [focusRequest]);
  const fields = (v: OutboundValues) => [
    "policy" as const,
    ...v.allowed.map((_, i) => `allowed.${i}` as const),
    ...v.denied.map((_, i) => `denied.${i}` as const),
  ];
  const save = useMutation({
    mutationFn: (v: OutboundValues) =>
      updateOutboundPolicy(
        {
          policy: v.policy,
          allowed: v.allowed.map((e) => e.trim()),
          denied: v.denied.map((e) => e.trim()),
        },
        { headers: { "If-Match": base.etag ?? "" } },
      ),
    onMutate: () => queryClient.cancelQueries({ queryKey: getGetOutboundPolicyQueryKey() }),
    onSuccess: (updated) => {
      queryClient.setQueryData(getGetOutboundPolicyQueryKey(), updated);
      setBase(updated);
      form.reset(formValues(updated));
      setSaved(true);
    },
    onError: (err, v) => {
      if (isApiError(err) && err.errors?.length) {
        setUnmatched(applyFieldErrors(err, form.setError, fields(v), (code) => code));
        setFocusRequest((n) => n + 1);
      }
    },
  });
  const reload = () => {
    save.reset();
    setSaved(false);
    setUnmatched([]);
    void queryClient
      .fetchQuery({ ...getGetOutboundPolicyQueryOptions(), staleTime: 0 })
      .then((fresh) => {
        setBase(fresh);
        form.reset(formValues(fresh));
      });
  };
  const fieldErrors = isApiError(save.error) && (save.error.errors?.length ?? 0) > 0;
  const { errors } = form.formState;
  const allowed = useWatch({ control: form.control, name: "allowed" });
  const denied = useWatch({ control: form.control, name: "denied" });
  const ruleLabel = (rule: string) => {
    switch (rule) {
      case "link-local":
        return t("organization.outbound.rule.linkLocal");
      case "cloud metadata address":
        return t("organization.outbound.rule.cloudMetadata");
      case "unspecified":
        return t("organization.outbound.rule.unspecified");
      case "multicast":
        return t("organization.outbound.rule.multicast");
      default:
        return rule;
    }
  };
  const policyLabel = (p: OutboundPolicyPolicy) =>
    p === "strict" ? t("organization.outbound.strict") : t("organization.outbound.standard");
  return (
    <form
      ref={formRef}
      noValidate
      className="flex flex-col gap-6"
      onSubmit={form.handleSubmit(
        (v) => {
          setSaved(false);
          setUnmatched([]);
          save.mutate(v);
        },
        () => setFocusRequest((n) => n + 1),
      )}
    >
      <Card>
        <CardContent>
          <fieldset className="flex flex-col gap-3">
            <legend className="mb-2 text-sm font-medium">
              {t("organization.outbound.policy")}
            </legend>
            {POLICIES.map((p) => (
              <div key={p} className="flex items-start gap-2">
                <input
                  id={`outbound-policy-${p}`}
                  type="radio"
                  value={p}
                  disabled={!canWrite && base.policy !== p}
                  className="mt-0.5 size-4 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
                  {...form.register("policy")}
                />
                <label htmlFor={`outbound-policy-${p}`} className="text-sm font-medium">
                  {policyLabel(p)}
                </label>
              </div>
            ))}
          </fieldset>
        </CardContent>
      </Card>
      <Card>
        <CardContent className="flex flex-col gap-6">
          <Controller
            control={form.control}
            name="allowed"
            render={({ field }) => (
              <NetworkListEditor
                id="outbound-allowed"
                label={t("organization.outbound.allowed")}
                hint={t("organization.outbound.entryHint")}
                addLabel={t("organization.outbound.addAllowed")}
                placeholder="10.0.0.0/8"
                value={field.value}
                readOnly={!canWrite}
                errors={allowed.map((_, i) => errorText(t, errors.allowed?.[i]))}
                onChange={field.onChange}
              />
            )}
          />
          <Controller
            control={form.control}
            name="denied"
            render={({ field }) => (
              <NetworkListEditor
                id="outbound-denied"
                label={t("organization.outbound.denied")}
                addLabel={t("organization.outbound.addDenied")}
                placeholder="*.example.org"
                value={field.value}
                readOnly={!canWrite}
                errors={denied.map((_, i) => errorText(t, errors.denied?.[i]))}
                onChange={field.onChange}
              />
            )}
          />
          <p className="text-sm font-medium">{t("organization.outbound.deniedWins")}</p>
        </CardContent>
      </Card>
      <Card role="region" aria-labelledby="outbound-always-blocked">
        <CardHeader>
          <CardTitle>
            <h2 id="outbound-always-blocked">{t("organization.outbound.alwaysBlocked")}</h2>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <ul className="flex flex-col gap-2 text-sm">
            {(base.always_blocked ?? []).map((text) => {
              const { rule, networks } = blockedRule(text);
              return (
                <li key={text} className="flex flex-col">
                  <span>{ruleLabel(rule)}</span>
                  {networks !== "" && (
                    <span className="font-mono text-muted-foreground wrap-anywhere">
                      {networks}
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
        </CardContent>
      </Card>
      {canWrite && (
        <SaveBar
          testId="outbound-status"
          state={{
            pending: save.isPending,
            saved,
            dirty: form.formState.isDirty,
            stale: (policy.etag !== base.etag && !save.isPending) || isStale(save.error),
            error: save.isError && !isStale(save.error) && !fieldErrors ? save.error : undefined,
            unmatched,
            reload,
          }}
        />
      )}
    </form>
  );
}

function OutboundView() {
  const { t } = useTranslation();
  const query = useGetOutboundPolicy();
  const canWrite = useCan("organization:write");
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <PageHeading title={t("organization.nav.outbound")} hint={t("organization.outbound.hint")} />
      {query.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <div className="flex max-w-2xl min-w-0 flex-col gap-6">
          {/* A viewer who cannot save follows every newer version; an Admin's form keeps the version it was read at. */}
          <OutboundForm
            key={canWrite ? "edit" : `view-${query.data.etag ?? ""}`}
            policy={query.data}
            canWrite={canWrite}
          />
        </div>
      )}
    </div>
  );
}

function OutboundPage() {
  return (
    <RequirePermission permission="organization:read">
      <OutboundView />
    </RequirePermission>
  );
}
