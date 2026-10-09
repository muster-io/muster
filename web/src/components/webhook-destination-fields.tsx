// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The fields of an outgoing webhook Destination (C-15.FR-1, FR-3, FR-10): the mode tabs "Events", "Template" and
// "Both"; in events mode the URL and the headers of the signed POST of each lifecycle event; in template mode the
// request builders "Create" and "Update", and "Open thread" and "Reply in thread" when added, with the extraction rules
// of "Create" and "Open thread"; "Both" shows both parts. The proxy form follows. An outgoing webhook has no Connection
// and no Destination check. The stored Destination's literal_credential warnings show at their fields while those
// still hold the stored value. Mentions are data the receiver formats, so the only chat-wide choice is everyone.

import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import type {
  Destination,
  HeaderTemplate,
  RequestTemplate,
  WebhookDestination,
  WebhookDestinationBaseMode,
  WebhookDestinationInput,
  WebhookTemplateConfig,
} from "../api/gen/model";
import type { DestinationKind, TypeFieldsProps } from "./destination-form";
import {
  type FieldNotes,
  HeaderEditor,
  ListProblem,
  TemplateInput,
  reference,
} from "./header-editor";
import {
  CheckField,
  type ProxyField,
  ProxyForm,
  type ProxyValues,
  proxyErrors,
  proxyInput,
  proxyValues,
} from "./proxy-form";
import { RequestBuilder, type RequestValues } from "./request-builder";
import { StatusTabs } from "./status-tabs";

export type WebhookMode = WebhookDestinationBaseMode;

export const MODES: readonly WebhookMode[] = ["events", "template", "both"];

/** The requests of the template mode, by their name in WebhookTemplateConfig. */
export type RequestName = keyof WebhookTemplateConfig;

export const REQUESTS: readonly RequestName[] = [
  "create",
  "update",
  "open_thread",
  "reply_in_thread",
];

/** The optional requests, which the form sends only when they are added. */
type OptionalRequest = "open_thread" | "reply_in_thread";

function isOptional(name: RequestName): name is OptionalRequest {
  return name === "open_thread" || name === "reply_in_thread";
}

/** The requests that take extraction rules. */
function extracts(name: RequestName): boolean {
  return name === "create" || name === "open_thread";
}

export interface WebhookFieldValues {
  mode: WebhookMode;
  events: { url: string; headers: HeaderTemplate[] };
  requests: Record<RequestName, RequestValues>;
  /** Whether "Open thread" and "Reply in thread" are added. */
  added: Record<OptionalRequest, boolean>;
  proxy: ProxyValues;
}

/** destination.webhook.limiter: 5 requests per second. */
export const DEFAULT_WEBHOOK_LIMITER = { limit: 5, per_seconds: 1 } as const;

function emptyRequest(name: RequestName): RequestValues {
  return {
    method: name === "update" ? "PUT" : "POST",
    url: "",
    headers: [],
    body: "",
    extract: [],
  };
}

function requestValues(name: RequestName, r: RequestTemplate | undefined): RequestValues {
  if (r === undefined) {
    return emptyRequest(name);
  }
  return {
    method: r.method,
    url: r.url,
    headers: r.headers.map((h) => ({ name: h.name, value: h.value })),
    body: r.body ?? "",
    extract: (r.extract ?? []).map((e) => ({ name: e.name, path: e.path })),
  };
}

function isWebhook(d: Destination | undefined): d is WebhookDestination {
  return d?.type === "webhook";
}

export function webhookValues(d: Destination | undefined): WebhookFieldValues {
  const w = isWebhook(d) ? d : undefined;
  const template = w?.template;
  return {
    mode: w?.mode ?? "events",
    events: {
      url: w?.events?.url ?? "",
      headers: (w?.events?.headers ?? []).map((h) => ({ name: h.name, value: h.value })),
    },
    requests: {
      create: requestValues("create", template?.create),
      update: requestValues("update", template?.update),
      open_thread: requestValues("open_thread", template?.open_thread),
      reply_in_thread: requestValues("reply_in_thread", template?.reply_in_thread),
    },
    added: {
      open_thread: template?.open_thread !== undefined,
      reply_in_thread: template?.reply_in_thread !== undefined,
    },
    proxy: proxyValues(w?.proxy),
  };
}

function sendsEvents(mode: WebhookMode): boolean {
  return mode !== "template";
}

function sendsTemplate(mode: WebhookMode): boolean {
  return mode !== "events";
}

/** The requests a save sends: "Create", "Update" and the added optional ones. */
function sentRequests(v: WebhookFieldValues): RequestName[] {
  return REQUESTS.filter((name) => !isOptional(name) || v.added[name]);
}

function requestInput(name: RequestName, r: RequestValues): RequestTemplate {
  const out: RequestTemplate = {
    method: r.method,
    url: r.url.trim(),
    headers: r.headers.map((h) => ({ name: h.name.trim(), value: h.value })),
    body: r.body === "" ? null : r.body,
  };
  if (extracts(name)) {
    out.extract = r.extract.map((e) => ({ name: e.name.trim(), path: e.path.trim() }));
  }
  return out;
}

/** The requests of a save as the API takes them: the events request, the request templates, or both. */
export function webhookRequests(
  v: WebhookFieldValues,
): Pick<WebhookDestinationInput, "events" | "template"> {
  const out: Pick<WebhookDestinationInput, "events" | "template"> = {};
  if (sendsEvents(v.mode)) {
    out.events = {
      url: v.events.url.trim(),
      headers: v.events.headers.map((h) => ({ name: h.name.trim(), value: h.value })),
    };
  }
  if (sendsTemplate(v.mode)) {
    const template: WebhookTemplateConfig = {
      create: requestInput("create", v.requests.create),
      update: requestInput("update", v.requests.update),
    };
    for (const name of sentRequests(v)) {
      if (isOptional(name)) {
        template[name] = requestInput(name, v.requests[name]);
      }
    }
    out.template = template;
  }
  return out;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** The value at a JSON pointer, such as /events/headers/0/value, of a value made of objects and arrays. */
export function valueAt(root: unknown, pointer: string): unknown {
  let at: unknown = root;
  for (const raw of pointer.split("/").slice(1)) {
    const key = raw.replace(/~1/g, "/").replace(/~0/g, "~");
    if (Array.isArray(at)) {
      at = at[Number(key)];
    } else if (isRecord(at)) {
      at = at[key];
    } else {
      return undefined;
    }
  }
  return at;
}

/** The names of the extracted values a template reads as .Response.<name> or index .Response "<name>". */
export function responseRefs(src: string): string[] {
  const names = new Set<string>();
  for (const m of src.matchAll(/\.Response\.([A-Za-z_][A-Za-z0-9_]*)/g)) {
    names.add(m[1] ?? "");
  }
  for (const m of src.matchAll(/index\s+\$?\.Response\s+"([^"]*)"/g)) {
    names.add(m[1] ?? "");
  }
  names.delete("");
  return [...names];
}

/**
 * The first extracted value the template src of a request reads that no extraction rule the request may read gives:
 * "Create" reads none, the others read those of "Create", and "Reply in thread" also those of "Open thread".
 */
export function unreadableValue(
  v: WebhookFieldValues,
  pointer: string,
  src: string,
): string | undefined {
  const name = REQUESTS.find((r) => pointer.startsWith(`/template/${r}/`));
  if (name === undefined) {
    return undefined;
  }
  const readable = new Set<string>();
  if (name !== "create") {
    for (const rule of v.requests.create.extract) {
      readable.add(rule.name.trim());
    }
  }
  if (name === "reply_in_thread" && v.added.open_thread) {
    for (const rule of v.requests.open_thread.extract) {
      readable.add(rule.name.trim());
    }
  }
  return responseRefs(src).find((ref) => !readable.has(ref));
}

/** The values a field at a pointer depends on: every request of both modes, whether added, and the proxy. */
function fieldsAt(v: WebhookFieldValues, pointer: string): unknown {
  if (pointer === "/mode") {
    return v.mode;
  }
  if (pointer.startsWith("/proxy")) {
    return v.proxy;
  }
  const all = webhookRequests({
    ...v,
    mode: "both",
    added: { open_thread: true, reply_in_thread: true },
  });
  const name = REQUESTS.find(
    (r) => pointer === `/template/${r}` || pointer.startsWith(`/template/${r}/`),
  );
  const added = name !== undefined && isOptional(name) ? v.added[name] : true;
  return [v.mode, added, valueAt(all, pointer)];
}

/** Whether a change touches the field at a pointer: its value, its request's being added, or the mode. */
export function webhookChanged(
  previous: WebhookFieldValues,
  next: WebhookFieldValues,
  pointer: string,
): boolean {
  return JSON.stringify(fieldsAt(previous, pointer)) !== JSON.stringify(fieldsAt(next, pointer));
}

function headerChecks(base: string, headers: HeaderTemplate[], errors: Record<string, string>) {
  headers.forEach((h, i) => {
    if (h.name.trim() === "") {
      errors[`${base}/${i}/name`] = "required";
    }
  });
}

/** The checks the server repeats on the fields it can check without a dry run, as codes by pointer. */
export function webhookErrors(v: WebhookFieldValues): Record<string, string> {
  const errors: Record<string, string> = {};
  if (sendsEvents(v.mode)) {
    if (v.events.url.trim() === "") {
      errors["/events/url"] = "required";
    }
    headerChecks("/events/headers", v.events.headers, errors);
  }
  if (sendsTemplate(v.mode)) {
    for (const name of sentRequests(v)) {
      const r = v.requests[name];
      const base = `/template/${name}`;
      if (r.url.trim() === "") {
        errors[`${base}/url`] = "required";
      }
      headerChecks(`${base}/headers`, r.headers, errors);
      if (extracts(name)) {
        r.extract.forEach((e, i) => {
          if (e.name.trim() === "") {
            errors[`${base}/extract/${i}/name`] = "required";
          }
          if (e.path.trim() === "") {
            errors[`${base}/extract/${i}/path`] = "required";
          }
        });
      }
    }
  }
  for (const [field, code] of Object.entries(proxyErrors(v.proxy))) {
    errors[`/proxy/${field}`] = code;
  }
  return errors;
}

export function modeName(t: TFunction, mode: WebhookMode): string {
  switch (mode) {
    case "events":
      return t("destinations.webhook.modes.events");
    case "template":
      return t("destinations.webhook.modes.template");
    default:
      return t("destinations.webhook.modes.both");
  }
}

function modeHint(t: TFunction, mode: WebhookMode): string {
  switch (mode) {
    case "events":
      return t("destinations.webhook.modeHints.events");
    case "template":
      return t("destinations.webhook.modeHints.template");
    default:
      return t("destinations.webhook.modeHints.both");
  }
}

export function requestName(t: TFunction, name: RequestName): string {
  switch (name) {
    case "create":
      return t("destinations.webhook.requests.create");
    case "update":
      return t("destinations.webhook.requests.update");
    case "open_thread":
      return t("destinations.webhook.requests.openThread");
    default:
      return t("destinations.webhook.requests.replyInThread");
  }
}

function requestHint(t: TFunction, name: RequestName): string {
  switch (name) {
    case "create":
      return t("destinations.webhook.requestHints.create");
    case "update":
      return t("destinations.webhook.requestHints.update", {
        ref: reference("Response", "<name>"),
      });
    case "open_thread":
      return t("destinations.webhook.requestHints.openThread");
    default:
      return t("destinations.webhook.requestHints.replyInThread");
  }
}

const PROXY_FIELDS: readonly ProxyField[] = ["type", "address", "username", "password"];

export function WebhookDestinationFields({
  id,
  value,
  onChange,
  errors,
  problems,
  disabled,
  destination,
}: TypeFieldsProps<WebhookFieldValues>) {
  const { t } = useTranslation();
  const stored = isWebhook(destination) ? destination : undefined;
  const current = webhookRequests(value);
  const storedRequests = stored === undefined ? {} : webhookRequests(webhookValues(stored));
  const literals = new Set(
    (stored?.warnings ?? [])
      .filter((w) => w.kind === "literal_credential")
      .flatMap((w) => (typeof w.field === "string" ? [w.field] : [])),
  );
  const notes: FieldNotes = {
    problem: (pointer) => problems[pointer],
    literal: (pointer) =>
      literals.has(pointer) && valueAt(current, pointer) === valueAt(storedRequests, pointer),
    unreadable: (pointer, src) => unreadableValue(value, pointer, src),
  };
  const proxyFieldErrors: Partial<Record<ProxyField, string>> = {};
  for (const field of PROXY_FIELDS) {
    const message = errors[`/proxy/${field}`];
    if (message !== undefined) {
      proxyFieldErrors[field] = message;
    }
  }
  const setRequest = (name: RequestName, next: RequestValues) =>
    onChange({ ...value, requests: { ...value.requests, [name]: next } });
  const panelId = `${id}-mode-panel`;
  return (
    <div className="flex min-w-0 flex-col gap-6" data-testid="webhook-fields">
      <div className="flex min-w-0 flex-col gap-2">
        <span aria-hidden="true" className="text-sm font-medium">
          {t("destinations.webhook.mode")}
        </span>
        <StatusTabs
          tabs={MODES}
          value={value.mode}
          label={t("destinations.webhook.mode")}
          panelId={panelId}
          labelOf={(mode) => modeName(t, mode)}
          onSelect={(mode) => onChange({ ...value, mode })}
        />
        <p id={`${id}-mode-hint`} className="text-sm text-muted-foreground">
          {modeHint(t, value.mode)}
        </p>
        {["/mode", "/events", "/template"].map((pointer) => (
          <ListProblem
            key={pointer}
            id={`${id}-${pointer.slice(1)}-error`}
            pointer={pointer}
            notes={notes}
          />
        ))}
      </div>
      <div
        id={panelId}
        role="tabpanel"
        aria-labelledby={`${panelId}-tab-${value.mode}`}
        className="flex min-w-0 flex-col gap-6"
      >
        {sendsEvents(value.mode) && (
          <section className="flex min-w-0 flex-col gap-4" data-testid="webhook-events">
            <h3 className="text-base font-semibold">{t("destinations.webhook.eventsTitle")}</h3>
            <TemplateInput
              id={`${id}-events-url`}
              label={t("destinations.webhook.url")}
              hint={t("destinations.webhook.eventsUrlHint", {
                ref: reference("Secrets", "token"),
              })}
              placeholder="https://automation.example.org/muster"
              value={value.events.url}
              pointer="/events/url"
              notes={notes}
              disabled={disabled}
              onChange={(url) => onChange({ ...value, events: { ...value.events, url } })}
            />
            <HeaderEditor
              id={`${id}-events`}
              base="/events/headers"
              value={value.events.headers}
              notes={notes}
              disabled={disabled}
              onChange={(headers) => onChange({ ...value, events: { ...value.events, headers } })}
            />
          </section>
        )}
        {sendsTemplate(value.mode) && (
          <section className="flex min-w-0 flex-col gap-4" data-testid="webhook-template">
            <h3 className="text-base font-semibold">{t("destinations.webhook.templateTitle")}</h3>
            <p className="text-sm text-muted-foreground">
              {t("destinations.webhook.templateHint", { ref: reference("Secrets", "token") })}
            </p>
            {REQUESTS.map((name) => {
              const optional = isOptional(name);
              const open = !optional || value.added[name];
              const builderId = `${id}-${name.replace(/_/g, "-")}`;
              return (
                <RequestBuilder
                  key={name}
                  id={builderId}
                  title={requestName(t, name)}
                  hint={requestHint(t, name)}
                  base={`/template/${name}`}
                  value={value.requests[name]}
                  extract={extracts(name)}
                  notes={notes}
                  disabled={disabled}
                  open={open}
                  action={
                    optional ? (
                      <CheckField
                        id={`${builderId}-added`}
                        label={t("destinations.webhook.addRequest", {
                          request: requestName(t, name),
                        })}
                        checked={value.added[name]}
                        disabled={disabled}
                        onChange={(checked) =>
                          onChange({ ...value, added: { ...value.added, [name]: checked } })
                        }
                      />
                    ) : undefined
                  }
                  onChange={(next) => setRequest(name, next)}
                />
              );
            })}
          </section>
        )}
      </div>
      <ProxyForm
        idPrefix={`${id}-proxy`}
        value={value.proxy}
        passwordStatus={stored?.proxy.password_status}
        errors={proxyFieldErrors}
        disabled={disabled}
        onChange={(proxy) => onChange({ ...value, proxy })}
      />
    </div>
  );
}

/**
 * The outgoing webhook type of the Destination form: no Connection and no check, Mentions as data with everyone,
 * groups and users, and the limiter destination.webhook.limiter.
 */
export const WEBHOOK_KIND: DestinationKind<WebhookFieldValues> = {
  type: "webhook",
  everyone: ["all"],
  groups: true,
  defaultLimiter: DEFAULT_WEBHOOK_LIMITER,
  limiterHint: (t) => t("destinations.webhook.limiterHint"),
  mentionsHint: (t) => t("mentions.everyoneHintWebhook"),
  groupPlaceholder: (t) => t("mentions.groupPlaceholderWebhook"),
  values: webhookValues,
  check: webhookErrors,
  input: (common, v) => ({
    type: "webhook",
    ...common,
    mode: v.mode,
    ...webhookRequests(v),
    proxy: proxyInput(v.proxy),
  }),
  pointers: ["/mode", "/events", "/template", "/proxy"],
  changed: webhookChanged,
  Fields: WebhookDestinationFields,
};
