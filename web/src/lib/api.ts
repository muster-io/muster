// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The glue between the generated client and the browser. Every request of the client goes through apiFetch: it sends
// the CSRF token of the current Session on mutating requests, turns an answer that is not 2xx into an ApiError that
// carries the Problem, and leaves the SPA for the sign-in page when the session is gone.

import { queryOptions, useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import type { FieldValues, Path, UseFormSetError } from "react-hook-form";

import type { AlertGroupRef, EntityRef, Problem, ProblemError, Session } from "../api/gen/model";

/** An answer that is not 2xx, with its RFC 9457 Problem. Clients branch on status, code and errors[].code only. */
export class ApiError extends Error implements Problem {
  readonly type: string;
  readonly title: string;
  readonly status: number;
  readonly code?: string;
  readonly errors?: ProblemError[];
  readonly retry_after_seconds?: number;
  readonly related_alert_group?: AlertGroupRef;
  readonly link_rules?: EntityRef[];

  constructor(status: number, problem: Partial<Problem>, retryAfter: number | undefined) {
    super(problem.title ?? `HTTP ${status}`);
    this.name = "ApiError";
    this.type = problem.type ?? "about:blank";
    this.title = problem.title ?? `HTTP ${status}`;
    this.status = status;
    this.code = problem.code;
    this.errors = problem.errors;
    this.retry_after_seconds = problem.retry_after_seconds ?? retryAfter;
    this.related_alert_group = problem.related_alert_group;
    this.link_rules = problem.link_rules;
  }
}

export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError;
}

/** The pages that need no session; the session handling never leaves them for the sign-in page. */
export const PUBLIC_PATHS: readonly string[] = ["/sign-in", "/password-setup"];

/** The reason the sign-in page explains, set when a session ended rather than never existed. */
export const SESSION_ENDED = "session_ended";

const ENDED_CODES = new Set(["session_expired", "oidc_session_ended"]);

const MUTATING = new Set(["POST", "PUT", "PATCH", "DELETE"]);

let csrfToken: string | undefined;

/** Keeps the CSRF token of a Session read or created, for the mutating requests that follow. */
export function rememberSession(session: Session | null): void {
  csrfToken = session?.csrf_token;
}

/**
 * Keeps a path to return to after sign-in only when it is a path of this application: a relative path that starts
 * with a single "/", so that a crafted link never sends the browser to another site.
 */
export function safeReturnTo(value: unknown): string | undefined {
  if (typeof value !== "string" || value.length === 0 || value.length > 2000) {
    return undefined;
  }
  if (!value.startsWith("/") || value.startsWith("//") || value.startsWith("/\\")) {
    return undefined;
  }
  for (let i = 0; i < value.length; i++) {
    if (value.charCodeAt(i) < 0x20) {
      return undefined;
    }
  }
  return value;
}

/** A path with return_to added when there is one. */
export function withReturnTo(path: string, returnTo: string | undefined): string {
  return returnTo === undefined
    ? path
    : `${path}?${new URLSearchParams({ return_to: returnTo }).toString()}`;
}

/**
 * The address of the sign-in page that returns to a page (by default the current one), with the reason when a session
 * ended.
 */
export function signInHref(
  reason?: string,
  pathname: string = window.location.pathname,
  search: string = window.location.search,
): string {
  const params = new URLSearchParams();
  if (reason) {
    params.set("reason", reason);
  }
  const limited = ["/sign-in/totp", "/totp-enrolment"];
  if (pathname !== "/" && !PUBLIC_PATHS.includes(pathname) && !limited.includes(pathname)) {
    params.set("return_to", pathname + search);
  }
  const query = params.toString();
  return query ? `/sign-in?${query}` : "/sign-in";
}

/** Leaves the SPA for the page of a limited session or the sign-in page; a full load also drops every cached answer. */
function leave(href: string): Promise<never> {
  window.location.assign(href);
  // The page unloads; the caller waits instead of showing an error for a moment.
  return new Promise<never>(() => {});
}

/**
 * Sends the browser where an answer says it belongs: the sign-in page on a 401 (except for wrong credentials, which
 * the form shows), the second factor or the enrolment on the 403 of a limited session. Returns undefined when the
 * answer is the caller's to show.
 */
function redirectFor(err: ApiError): string | undefined {
  const path = window.location.pathname;
  if (err.status === 401 && err.code !== "invalid_credentials" && !PUBLIC_PATHS.includes(path)) {
    return signInHref(
      err.code !== undefined && ENDED_CODES.has(err.code) ? SESSION_ENDED : undefined,
    );
  }
  if (err.status === 403 && err.code === "totp_required" && path !== "/sign-in/totp") {
    return "/sign-in/totp";
  }
  if (err.status === 403 && err.code === "totp_enrolment_required" && path !== "/totp-enrolment") {
    return "/totp-enrolment";
  }
  return undefined;
}

async function readBody(res: Response): Promise<unknown> {
  if ([204, 205, 304].includes(res.status)) {
    return undefined;
  }
  const text = await res.text();
  if (text === "") {
    return undefined;
  }
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return undefined;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** The fields of a Problem body that a client branches on; anything malformed is left out. */
function problemOf(body: unknown): Partial<Problem> {
  if (!isRecord(body)) {
    return {};
  }
  const text = (key: string) => (typeof body[key] === "string" ? body[key] : undefined);
  const errors = Array.isArray(body.errors)
    ? body.errors.filter(
        (e): e is ProblemError =>
          isRecord(e) && typeof e.pointer === "string" && typeof e.code === "string",
      )
    : undefined;
  const related = body.related_alert_group;
  return {
    type: text("type"),
    title: text("title"),
    code: text("code"),
    errors,
    retry_after_seconds:
      typeof body.retry_after_seconds === "number" ? body.retry_after_seconds : undefined,
    related_alert_group:
      isRecord(related) && typeof related.id === "string" && typeof related.number === "number"
        ? { id: related.id, number: related.number }
        : undefined,
    link_rules: Array.isArray(body.link_rules)
      ? body.link_rules.flatMap((r) =>
          isRecord(r) && typeof r.id === "string" && typeof r.name === "string"
            ? [{ id: r.id, name: r.name }]
            : [],
        )
      : undefined,
  };
}

function toApiError(res: Response, body: unknown): ApiError {
  const problem = problemOf(body);
  const header = Number.parseInt(res.headers.get("Retry-After") ?? "", 10);
  return new ApiError(res.status, problem, Number.isFinite(header) ? header : undefined);
}

function isSession(body: unknown): body is Session {
  return (
    isRecord(body) &&
    typeof body.csrf_token === "string" &&
    typeof body.state === "string" &&
    isRecord(body.user)
  );
}

function plainHeaders(
  init: RequestInit["headers"] | Record<string, string | readonly string[]>,
): Headers {
  const headers = new Headers();
  if (init === undefined) {
    return headers;
  }
  if (init instanceof Headers || Array.isArray(init)) {
    new Headers(init).forEach((value, name) => headers.set(name, value));
    return headers;
  }
  for (const [name, value] of Object.entries(init)) {
    headers.set(name, typeof value === "string" ? value : value.join(", "));
  }
  return headers;
}

/** Sends a request of the client and resolves with the parsed body and the headers of a 2xx answer. */
async function send(
  url: string,
  options: RequestInit,
): Promise<{ body: unknown; headers: Headers }> {
  const method = (options.method ?? "GET").toUpperCase();
  const headers = plainHeaders(options.headers);
  headers.set("Accept", "application/json, application/problem+json");
  if (MUTATING.has(method) && csrfToken !== undefined) {
    headers.set("X-CSRF-Token", csrfToken);
  }
  const res = await fetch(url, { ...options, method, headers, credentials: "same-origin" });
  const body = await readBody(res);
  if (!res.ok) {
    const err = toApiError(res, body);
    const href = redirectFor(err);
    if (href !== undefined) {
      return leave(href);
    }
    throw err;
  }
  if (isSession(body)) {
    rememberSession(body);
  }
  return { body, headers: res.headers };
}

/** The fetch of the generated client (the orval mutator). Resolves with the parsed body of a 2xx answer. */
export async function apiFetch<T>(url: string, options: RequestInit): Promise<T> {
  const { body } = await send(url, options);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the generated client names the body's type from the spec
  return body as T;
}

/** A 2xx answer with the version its ETag header names. */
export interface Tagged<T> {
  data: T;
  etag: string;
}

/**
 * apiFetch for a resource whose ETag is only in the header, such as the Routes list, whose ETag covers the order:
 * resolves with the body and the ETag, for the If-Match of the next change.
 */
export async function apiFetchTagged<T>(url: string, options: RequestInit): Promise<Tagged<T>> {
  const { body, headers } = await send(url, options);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the caller names the body's type from the spec
  return { data: body as T, etag: headers.get("ETag") ?? "" };
}

/** The current session, or null with whether it ended (rather than never existed); never leaves the page. */
export interface SessionRead {
  session: Session | null;
  ended: boolean;
}

export async function readSession(signal?: AbortSignal): Promise<SessionRead> {
  const res = await fetch("/api/v1/sessions/current", {
    headers: { Accept: "application/json, application/problem+json" },
    credentials: "same-origin",
    signal,
  });
  const body = await readBody(res);
  if (res.status === 401) {
    rememberSession(null);
    const code = problemOf(body).code;
    return { session: null, ended: code !== undefined && ENDED_CODES.has(code) };
  }
  if (!res.ok || !isSession(body)) {
    throw toApiError(res, body);
  }
  rememberSession(body);
  return { session: body, ended: false };
}

/** The query key under which the SPA keeps the SessionRead. */
export const SESSION_QUERY_KEY = ["session"] as const;

/** Reads the session afresh: the root route fetches it on every navigation, so an ended session is noticed. */
export const sessionQuery = queryOptions({
  queryKey: SESSION_QUERY_KEY,
  queryFn: ({ signal }) => readSession(signal),
  staleTime: 0,
});

/** The current session, as the root route last read it; null on the public pages. */
export function useSession(): Session | null {
  const { data } = useQuery({ ...sessionQuery, staleTime: Number.POSITIVE_INFINITY });
  return data?.session ?? null;
}

/**
 * Puts the field errors of a 400 or 422 Problem on the fields of a form; pointer "/new_password" names the field
 * new_password. Returns the codes that matched no field, for the form to show as a whole.
 */
export function applyFieldErrors<T extends FieldValues>(
  err: ApiError,
  setError: UseFormSetError<T>,
  fields: readonly Path<T>[],
  message: (code: string) => string,
): string[] {
  const unmatched: string[] = [];
  for (const item of err.errors ?? []) {
    const pointer = item.pointer.replace(/^\//, "").split("/")[0];
    const name = fields.find((f) => f === pointer);
    if (name !== undefined) {
      setError(name, { type: item.code, message: message(item.code) });
    } else {
      unmatched.push(item.code);
    }
  }
  return unmatched;
}

/** The text of a refusal that a form shows as a whole. */
export function problemText(t: TFunction, err: unknown): string {
  if (isApiError(err)) {
    if (err.status === 429) {
      return t("errors.tooManyAttempts", { count: Math.max(1, err.retry_after_seconds ?? 1) });
    }
    if (err.status === 401 && err.code === "invalid_credentials") {
      return t("errors.wrongCredentials");
    }
    if (err.status === 403 && err.code === "csrf_invalid") {
      return t("errors.reload");
    }
    if (err.status === 403) {
      return t("errors.forbidden");
    }
    if (err.status === 404) {
      return t("errors.gone");
    }
    if (err.status === 412) {
      return t("errors.stale");
    }
    switch (err.code) {
      case "last_admin":
        return t("errors.lastAdmin");
      case "role_locked":
        return t("errors.roleLocked");
      case "name_taken":
        return t("errors.loginTaken");
      case "local_user_only":
        return t("errors.localUserOnly");
      case "oidc_not_linked":
        return t("errors.oidcNotLinked");
      default:
        break;
    }
  }
  return t("errors.generic");
}

/** A save refused because the item changed since it was read (412): the form offers to reload it. */
export function isStale(err: unknown): boolean {
  return isApiError(err) && err.status === 412;
}

/** The text of a field error code of a Problem (errors[].code). */
export function fieldErrorText(t: TFunction, code: string): string {
  switch (code) {
    case "required":
      return t("fieldErrors.required");
    case "too_short":
      return t("fieldErrors.tooShort");
    case "too_long":
      return t("fieldErrors.tooLong");
    case "invalid_format":
      return t("fieldErrors.invalidFormat");
    case "mismatch":
      return t("fieldErrors.mismatch");
    case "unsupported":
      return t("fieldErrors.unsupported");
    default:
      return t("fieldErrors.invalid");
  }
}
