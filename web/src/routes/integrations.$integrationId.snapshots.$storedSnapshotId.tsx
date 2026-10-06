// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Stored Snapshot viewer (C-05.FR-7): when it was received, its content type, its processing state with the error,
// and the body exactly as received with "Copy". The body comes from whoever held a token and is shown as text only;
// characters that would hide or reorder text are shown as escapes. A body that is not valid UTF-8 is shown as base64.
// A large body is shown in part, so that the page stays responsive; "Copy" always copies all of it.

import { Link, createFileRoute } from "@tanstack/react-router";
import { ArrowLeftIcon, TriangleAlertIcon } from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { useGetStoredSnapshot } from "../api/gen/endpoints/integrations/integrations";
import type { StoredSnapshot } from "../api/gen/model";
import { RequirePermission } from "../components/app-shell";
import { CopyBlock } from "../components/integration-token-dialog";
import { formatBytes, snapshotStateText } from "../components/stored-snapshots";
import { Button, buttonVariants } from "../components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "../components/ui/card";
import { problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";

export const Route = createFileRoute("/integrations/$integrationId/snapshots/$storedSnapshotId")({
  staticData: { shell: true },
  component: StoredSnapshotPage,
});

/** How much of a body is shown at first, in characters. */
const PREVIEW_CHARS = 64 * 1024;

/** The largest body shown in full or formatted as JSON on request, in characters; beyond it only "Copy" has it all. */
const FULL_CHARS = 2 * 1024 * 1024;

/**
 * Control, format and unassigned characters, line and paragraph separators, lone surrogates and the fillers and
 * variation selectors that render as nothing: shown as escapes, so that a body cannot hide or reorder the text around
 * them. Tabs and line breaks stay.
 */
const INVISIBLE =
  // oxlint-disable-next-line eslint/no-misleading-character-class -- each variation selector is matched alone, on purpose
  /[\p{Cc}\p{Cf}\p{Cs}\p{Cn}\p{Zl}\p{Zp}\u034F\u115F\u1160\u3164\uFFA0\uFE00-\uFE0F\u{E0100}-\u{E01EF}]/gu;

const KEPT = new Set(["\t", "\n", "\r"]);

/** The escape of a character: \uXXXX, or \u{XXXXX} beyond the Basic Multilingual Plane. */
function escaped(c: string): string {
  const hex = (c.codePointAt(0) ?? 0).toString(16).toUpperCase();
  return hex.length <= 4 ? `\\u${hex.padStart(4, "0")}` : `\\u{${hex}}`;
}

/** Text with its invisible characters shown as marked escapes, which a literal "\u202E" in the text is not. */
function Visible({ text }: { text: string }) {
  const parts: ReactNode[] = [];
  let last = 0;
  for (const match of text.matchAll(INVISIBLE)) {
    const c = match[0];
    if (KEPT.has(c)) {
      continue;
    }
    if (match.index > last) {
      parts.push(text.slice(last, match.index));
    }
    parts.push(
      <span
        key={match.index}
        className="rounded-sm bg-amber-100 px-0.5 text-amber-900 dark:bg-amber-900/40 dark:text-amber-100"
        data-escape=""
      >
        {escaped(c)}
      </span>,
    );
    last = match.index + c.length;
  }
  parts.push(text.slice(last));
  return <>{parts}</>;
}

/** The first characters of a text, at most limit, never ending inside a surrogate pair. */
function head(text: string, limit: number): string {
  if (text.length <= limit) {
    return text;
  }
  const code = text.charCodeAt(limit - 1);
  return text.slice(0, code >= 0xd800 && code <= 0xdbff ? limit - 1 : limit);
}

/** The body formatted as JSON with two spaces, or null when it is not JSON or too large to format. */
function formattedJson(snapshot: StoredSnapshot): string | null {
  if (snapshot.body_encoding !== "utf8" || snapshot.body.length > FULL_CHARS) {
    return null;
  }
  try {
    return JSON.stringify(JSON.parse(snapshot.body), null, 2);
  } catch {
    return null;
  }
}

function Body({ snapshot }: { snapshot: StoredSnapshot }) {
  const { t, i18n } = useTranslation();
  const locale = i18n.resolvedLanguage ?? "en";
  const [format, setFormat] = useState(false);
  const [all, setAll] = useState(false);
  // Formatting parses the body once, when it is first asked for.
  const formatted = useMemo(() => (format ? formattedJson(snapshot) : null), [format, snapshot]);
  const json = useMemo(
    () =>
      snapshot.body_encoding === "utf8" &&
      snapshot.body.length <= FULL_CHARS &&
      /^\s*[[{]/.test(snapshot.body),
    [snapshot],
  );
  const text = formatted ?? snapshot.body;
  const limit = all && text.length <= FULL_CHARS ? text.length : PREVIEW_CHARS;
  const shown = head(text, limit);
  const actions: ReactNode = json && (
    <Button variant="outline" size="sm" aria-pressed={format} onClick={() => setFormat((f) => !f)}>
      {t("snapshots.viewer.formatJson")}
    </Button>
  );
  return (
    <div className="flex flex-col gap-3">
      {snapshot.body_encoding === "base64" && (
        <p className="flex items-start gap-1.5 text-sm" data-testid="snapshot-base64">
          <TriangleAlertIcon
            aria-hidden="true"
            className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
          />
          <span>{t("snapshots.viewer.base64")}</span>
        </p>
      )}
      {format && (
        <p className="text-sm text-muted-foreground" role="status">
          {formatted === null ? t("snapshots.viewer.notJson") : t("snapshots.viewer.formattedNote")}
        </p>
      )}
      <CopyBlock
        id="snapshot-body"
        label={t("snapshots.viewer.body")}
        text={snapshot.body}
        actions={actions}
        testId="snapshot-body"
        className="max-h-[70vh]"
      >
        <Visible text={shown} />
      </CopyBlock>
      {shown.length < text.length && (
        <div className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground">
          <span role="status">
            {t("snapshots.viewer.partial", {
              count: text.length,
              shown: new Intl.NumberFormat(locale).format(shown.length),
              total: new Intl.NumberFormat(locale).format(text.length),
            })}
          </span>
          {text.length <= FULL_CHARS && (
            <Button variant="link" className="h-auto px-0" onClick={() => setAll(true)}>
              {t("snapshots.viewer.showAll")}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

function Details({ snapshot }: { snapshot: StoredSnapshot }) {
  const { t, i18n } = useTranslation();
  const { dateTime } = useTimeFormat();
  const locale = i18n.resolvedLanguage ?? "en";
  const rows: [string, ReactNode, string][] = [
    [
      t("snapshots.viewer.contentType"),
      snapshot.content_type ? (
        <code key="type" className="font-mono text-xs wrap-anywhere">
          <Visible text={snapshot.content_type} />
        </code>
      ) : (
        <span key="type" className="text-muted-foreground">
          {t("snapshots.viewer.noContentType")}
        </span>
      ),
      "content-type",
    ],
    [
      t("snapshots.columns.state"),
      <span key="state" className={snapshot.state === "failed" ? "text-destructive" : undefined}>
        {snapshotStateText(t, snapshot)}
      </span>,
      "state",
    ],
    [t("snapshots.columns.size"), formatBytes(t, snapshot.size_bytes, locale), "size"],
    [
      t("snapshots.columns.groupKey"),
      snapshot.group_key ? (
        <code key="group-key" className="font-mono text-xs wrap-anywhere">
          <Visible text={snapshot.group_key} />
        </code>
      ) : (
        "—"
      ),
      "group-key",
    ],
    [
      t("snapshots.columns.alerts"),
      snapshot.alert_count === null || snapshot.alert_count === undefined
        ? "—"
        : String(snapshot.alert_count),
      "alerts",
    ],
  ];
  if (snapshot.processed_at) {
    rows.push([t("snapshots.viewer.processed"), dateTime(snapshot.processed_at), "processed"]);
  }
  return (
    <dl className="grid gap-x-4 gap-y-2 text-sm sm:grid-cols-[auto_1fr]">
      {rows.map(([label, value, id]) => (
        <div key={id} className="contents">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="min-w-0 break-words" data-testid={`snapshot-${id}`}>
            {value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

function StoredSnapshotView({
  integrationId,
  storedSnapshotId,
}: {
  integrationId: string;
  storedSnapshotId: string;
}) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  // A Stored Snapshot never changes but for its state, and its body may be large: it is read once, not on every focus.
  const query = useGetStoredSnapshot(storedSnapshotId, {
    query: { staleTime: Number.POSITIVE_INFINITY, refetchOnWindowFocus: false },
  });
  const snapshot = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/integrations/$integrationId"
          params={{ integrationId: snapshot?.integration.id ?? integrationId }}
          className={buttonVariants({ variant: "link", className: "w-fit max-w-full px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          <span className="truncate">
            {snapshot?.integration.name ?? t("integrations.page.title")}
          </span>
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t("snapshots.viewer.title")}</h1>
        {snapshot !== undefined && (
          <p className="text-muted-foreground" data-testid="snapshot-received">
            {t("snapshots.viewer.received", { time: dateTime(snapshot.received_at) })}
          </p>
        )}
      </div>
      {snapshot === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>{t("integrations.page.details")}</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-6">
            <Details snapshot={snapshot} />
            <Body snapshot={snapshot} />
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function StoredSnapshotPage() {
  const { integrationId, storedSnapshotId } = Route.useParams();
  return (
    <RequirePermission permission="stored-snapshots:read">
      <StoredSnapshotView integrationId={integrationId} storedSnapshotId={storedSnapshotId} />
    </RequirePermission>
  );
}
