// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A request of a Destination test or preview (C-16.FR-2, FR-4): the method and the URL, the headers and the body as
// code. The server masks every Secret, the Signing secret and the tokens as [redacted] before the request reaches the
// page; everything here is shown as text only, and a JSON body is indented to be read.

import { useTranslation } from "react-i18next";

import type { RenderedRequest as Request } from "../api/gen/model";
import { cn } from "./ui/utils";

/**
 * A JSON body indented by two spaces when that changes nothing but the spacing: a body that reading and writing it
 * again would change — a number beyond the precision of JavaScript, a key given twice, an escape other than the ones Go
 * writes for <, > and & — is shown exactly as it is.
 */
export function readableBody(body: string): string {
  const trimmed = body.trim();
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) {
    return body;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return body;
  }
  const plain = trimmed
    .replaceAll("\\u003c", "<")
    .replaceAll("\\u003e", ">")
    .replaceAll("\\u0026", "&");
  return JSON.stringify(parsed) === plain ? JSON.stringify(parsed, null, 2) : body;
}

/** Untrusted text in a code block that wraps instead of making the page scroll sideways. */
export function CodeText({
  text,
  className,
  testId,
}: {
  text: string;
  className?: string;
  testId?: string;
}) {
  return (
    <pre
      className={cn(
        "max-h-80 overflow-y-auto rounded-md border bg-muted/40 p-2 font-mono text-xs leading-5 wrap-anywhere whitespace-pre-wrap",
        className,
      )}
      data-testid={testId}
    >
      <code>{text}</code>
    </pre>
  );
}

export function RenderedRequest({ request }: { request: Request }) {
  const { t } = useTranslation();
  const body = request.body ?? "";
  return (
    <div className="flex min-w-0 flex-col gap-2" data-testid="rendered-request">
      <p className="font-mono text-xs wrap-anywhere" data-testid="rendered-request-line">
        <span className="font-semibold">{request.method}</span> {request.url}
      </p>
      {request.headers.length > 0 && (
        <dl
          className="flex min-w-0 flex-col gap-0.5 font-mono text-xs"
          aria-label={t("destinationTest.request.headers")}
        >
          {request.headers.map((h, index) => (
            <div key={`${h.name}-${index}`} className="wrap-anywhere" data-testid="rendered-header">
              <dt className="inline font-semibold">{h.name}:</dt>{" "}
              <dd className="inline">{h.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {body === "" ? (
        <p className="text-xs text-muted-foreground">{t("destinationTest.request.noBody")}</p>
      ) : (
        <CodeText text={readableBody(body)} testId="rendered-request-body" />
      )}
    </div>
  );
}
