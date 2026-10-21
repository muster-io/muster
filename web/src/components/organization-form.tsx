// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// One resource, several pages (C-20.FR-1): every Organization page edits its slice of the Organization and sends the
// whole update as it was read, with the version the page was read at as If-Match, so a save over a newer version shows
// a conflict instead of overwriting it. A saved page says "Saved."; the field errors of a refusal show at their fields.
// Each page's title links to the guide of its entry of the sub-navigation (C-21.FR-11).

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRouterState } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import {
  type BaseSyntheticEvent,
  type ReactNode,
  type RefObject,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  type DefaultValues,
  type FieldValues,
  type Path,
  type Resolver,
  type UseFormReturn,
  useForm,
} from "react-hook-form";
import { useTranslation } from "react-i18next";

import {
  getGetOrganizationQueryKey,
  getGetOrganizationQueryOptions,
  updateOrganization,
  useGetOrganization,
} from "../api/gen/endpoints/organization/organization";
import type {
  Organization,
  OrganizationBase,
  OrganizationInput,
  Permission,
} from "../api/gen/model";
import { applyFieldErrors, fieldErrorText, isApiError, isStale, problemText } from "../lib/api";
import { RequirePermission } from "./app-shell";
import { PageTitle } from "./docs-link";
import { organizationDocs } from "./organization-nav";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Label } from "./ui/label";

/** What a page changes of the Organization; every other field is sent as it was read. */
export type OrganizationPatch = Partial<OrganizationBase> & {
  outgoing_heartbeat?: OrganizationInput["outgoing_heartbeat"];
};

/** The Organization as an update: everything as read, the stored Secrets kept, the patch applied. */
export function organizationInput(
  org: Organization,
  patch: OrganizationPatch = {},
): OrganizationInput {
  const { id: _id, etag: _etag, outgoing_heartbeat: heartbeat, ...rest } = org;
  const { password_status: _password, ...proxy } = heartbeat.proxy;
  return { ...rest, outgoing_heartbeat: { proxy }, ...patch };
}

/** The state of a page's save, which SaveBar shows. */
export interface SaveState {
  pending: boolean;
  saved: boolean;
  dirty: boolean;
  /** A newer version exists, or the save was refused for one (412). */
  stale: boolean;
  /** A refusal that is neither a conflict nor field errors. */
  error: unknown;
  /** The codes of field errors that match no field of the page. */
  unmatched: readonly string[];
  reload: () => void;
}

export interface OrganizationFormOptions<V extends FieldValues> {
  organization: Organization;
  /** The form values of an Organization as read. */
  values: (org: Organization) => V;
  /** The page's slice of the update. */
  patch: (values: V, base: Organization) => OrganizationPatch;
  /** The form fields that show their own errors; a field error at a pointer under none of them is listed with Save. */
  fields: (values: V) => readonly Path<V>[];
  resolver?: Resolver<V>;
  onSaved?: (org: Organization) => void;
}

export interface OrganizationForm<V extends FieldValues> {
  form: UseFormReturn<V>;
  /** The version the form was read at. */
  base: Organization;
  formRef: RefObject<HTMLFormElement | null>;
  onSubmit: (event?: BaseSyntheticEvent) => Promise<void>;
  state: SaveState;
}

/**
 * The form of one Organization page. The version the form was read at stays until the page saves or the Admin reloads:
 * a newer version on the server never replaces what the Admin sees unasked.
 */
export function useOrganizationForm<V extends FieldValues>(
  options: OrganizationFormOptions<V>,
): OrganizationForm<V> {
  const { organization, values, patch, fields, resolver, onSaved } = options;
  const queryClient = useQueryClient();
  const [base, setBase] = useState(organization);
  const [saved, setSaved] = useState(false);
  const [unmatched, setUnmatched] = useState<string[]>([]);
  const [focusRequest, setFocusRequest] = useState(0);
  const formRef = useRef<HTMLFormElement>(null);
  const form = useForm<V>({
    resolver,
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- whole values are default values of the form
    defaultValues: values(organization) as DefaultValues<V>,
  });
  const save = useMutation({
    mutationFn: (v: V) =>
      updateOrganization(organizationInput(base, patch(v, base)), {
        headers: { "If-Match": base.etag },
      }),
    // A read still on its way (a live hint) must not bring back the version this save replaces.
    onMutate: () => queryClient.cancelQueries({ queryKey: getGetOrganizationQueryKey() }),
    onSuccess: (updated) => {
      queryClient.setQueryData(getGetOrganizationQueryKey(), updated);
      setBase(updated);
      form.reset(values(updated));
      setSaved(true);
      onSaved?.(updated);
    },
    onError: (err, v) => {
      if (isApiError(err) && err.errors?.length) {
        // The fields keep the codes as their messages, as the page's own checks do; errorText words them.
        setUnmatched(applyFieldErrors(err, form.setError, fields(v), (code) => code));
        setFocusRequest((n) => n + 1);
      }
    },
  });
  // After a refused save, the focus goes to the first marked field.
  useEffect(() => {
    if (focusRequest > 0) {
      formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
    }
  }, [focusRequest]);
  const reload = () => {
    save.reset();
    setSaved(false);
    setUnmatched([]);
    void queryClient
      .fetchQuery({ ...getGetOrganizationQueryOptions(), staleTime: 0 })
      .then((fresh) => {
        setBase(fresh);
        form.reset(values(fresh));
      });
  };
  const onSubmit = form.handleSubmit(
    (v) => {
      setSaved(false);
      setUnmatched([]);
      save.mutate(v);
    },
    () => setFocusRequest((n) => n + 1),
  );
  const fieldErrors = isApiError(save.error) && (save.error.errors?.length ?? 0) > 0;
  return {
    form,
    base,
    formRef,
    onSubmit,
    state: {
      pending: save.isPending,
      saved,
      dirty: form.formState.isDirty,
      stale: (organization.etag !== base.etag && !save.isPending) || isStale(save.error),
      error: save.isError && !isStale(save.error) && !fieldErrors ? save.error : undefined,
      unmatched,
      reload,
    },
  };
}

/** The text of a field error, whose message is its code; undefined without an error. */
export function errorText(
  t: TFunction,
  error: { message?: string } | undefined,
): string | undefined {
  return error === undefined ? undefined : fieldErrorText(t, error.message ?? "");
}

/** The end of a settings form: the conflict with its Reload, a refusal, Save and "Saved.". */
export function SaveBar({ state, testId }: { state: SaveState; testId: string }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-3">
      {state.stale && (
        <Alert variant="destructive">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>{t("errors.stale")}</span>
            <Button variant="outline" size="sm" onClick={state.reload}>
              {t("common.reload")}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {state.error !== undefined && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, state.error)}
          </AlertDescription>
        </Alert>
      )}
      {state.unmatched.length > 0 && (
        <ul className="list-disc pl-5 text-sm text-destructive" role="alert">
          {state.unmatched.map((code, i) => (
            <li key={`${code}-${i}`}>{fieldErrorText(t, code)}</li>
          ))}
        </ul>
      )}
      <div className="flex items-center gap-3">
        <Button type="submit" disabled={state.pending}>
          {t("common.save")}
        </Button>
        <span role="status" className="text-sm text-muted-foreground" data-testid={testId}>
          {state.saved && !state.dirty ? t("common.saved") : ""}
        </span>
      </div>
    </div>
  );
}

/** A labelled field with its hint and its error, which the control names through aria-describedby. */
export function Field({
  id,
  label,
  hint,
  error,
  children,
}: {
  id: string;
  label: string;
  hint?: ReactNode;
  error?: string;
  children: (control: {
    id: string;
    "aria-invalid": boolean;
    "aria-describedby": string | undefined;
  }) => ReactNode;
}) {
  const described =
    [hint === undefined ? null : `${id}-hint`, error === undefined ? null : `${id}-error`]
      .filter((v) => v !== null)
      .join(" ") || undefined;
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children({ id, "aria-invalid": error !== undefined, "aria-describedby": described })}
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
}

/** An Organization page: its heading, then its content once the Organization is read. */
export function OrganizationPage({
  title,
  hint,
  permission = "organization:write",
  children,
}: {
  title: string;
  hint?: string;
  permission?: Permission;
  children: (organization: Organization) => ReactNode;
}) {
  return (
    <RequirePermission permission={permission}>
      <OrganizationView title={title} hint={hint}>
        {children}
      </OrganizationView>
    </RequirePermission>
  );
}

function OrganizationView({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: (organization: Organization) => ReactNode;
}) {
  const { t } = useTranslation();
  const query = useGetOrganization();
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <PageHeading title={title} hint={hint} />
      {query.data === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <div className="flex max-w-2xl min-w-0 flex-col gap-6">{children(query.data)}</div>
      )}
    </div>
  );
}

/** The heading of an Organization page, with the Guide link of its entry of the sub-navigation. */
export function PageHeading({ title, hint }: { title: string; hint?: string }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const docs = organizationDocs(pathname);
  return (
    <div className="flex flex-col gap-1">
      {docs === undefined ? (
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
      ) : (
        <PageTitle docs={docs}>{title}</PageTitle>
      )}
      {hint !== undefined && <p className="text-muted-foreground">{hint}</p>}
    </div>
  );
}
