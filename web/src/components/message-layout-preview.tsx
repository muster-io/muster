// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The compact layout of a Root message as each messenger lays it out (C-29.FR-4, FR-8, AC-7), drawn in the browser from
// the `layout` of previewTemplate: a Mattermost attachment (title, short fields, text, key labels, Alerts, people,
// notices, links), a Slack message (a header, a section with fields, context lines), a Telegram message (lines
// "{name}: {value}") and, for a Bridge, the layout as JSON beside the CommonMark body. Every value of the layout is plain
// text, drawn as text and never as markup; a link opens only when it is http(s). The buttons come from the Destination
// and are not part of the layout.

import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { MessageLayout, MessageLayoutAlerts, MessageLayoutPerson } from "../api/gen/model";
import { ExternalLink } from "./template-preview";
import { cn } from "./ui/utils";

/** The markup of a messenger whose layout is drawn from the structured data. */
export type LayoutMarkup = "markdown" | "mrkdwn" | "html";

// The layout carries no flag for a label whose firing Alerts differ: its value is then "N values" in the Route's
// language, and the line shows `name: N values` in place of `name=value`.
const MANY_VALUES = /^\d+ (?:values?|значени(?:е|я|й))$/u;

/** The key labels as the line shows them: `system=kubernetes`, `namespace: 2 values`. */
export function keyLabelTexts(layout: MessageLayout): string[] {
  return layout.labels.map((l) =>
    MANY_VALUES.test(l.value) ? `${l.key}: ${l.value}` : `${l.key}=${l.value}`,
  );
}

function personText(p: MessageLayoutPerson): string {
  return p.team === undefined ? p.name : `${p.name} (${p.team.name})`;
}

/** The people line: "Assignees: @ivan (devops), @petr (payments)". */
function peopleText(t: (key: string) => string, layout: MessageLayout): string {
  return layout.assignees.length === 0
    ? ""
    : `${t("templates.layout.assignees")}: ${layout.assignees.map(personText).join(", ")}`;
}

/** The Alerts of the layout as a line: "pod: a, b …and 2 more", resolved values struck through. */
function AlertsText({ alerts }: { alerts: MessageLayoutAlerts }) {
  const { t } = useTranslation();
  const more = Math.max(alerts.total - alerts.values.length, 0);
  return (
    <span data-testid="layout-alerts">
      {alerts.label}:{" "}
      {alerts.values.map((v, index) => (
        <span key={`${v.value}-${index}`}>
          {index > 0 && ", "}
          {v.resolved ? <s>{v.value}</s> : v.value}
        </span>
      ))}
      {more > 0 && ` ${t("templates.layout.more", { more })}`}
    </span>
  );
}

function Links({ layout }: { layout: MessageLayout }) {
  return (
    <span data-testid="layout-links">
      {layout.links.map((link, index) => (
        <span key={`${link.label}-${index}`}>
          {index > 0 && " · "}
          <ExternalLink href={link.url}>{link.label}</ExternalLink>
        </span>
      ))}
    </span>
  );
}

const STATUS_BAR = {
  firing: "border-l-destructive",
  acknowledged: "border-l-primary",
  snoozed: "border-l-muted-foreground",
  resolved: "border-l-emerald-500",
} as const;

function titleText(layout: MessageLayout): string {
  return `#${layout.number} ${layout.title}`;
}

function summaryTexts(layout: MessageLayout): string[] {
  return [layout.summary, layout.description].filter((s) => s !== "");
}

/** The first link of the layout, "Open in Muster", for the title to open. */
function titleLink(layout: MessageLayout, children: ReactNode): ReactNode {
  const first = layout.links[0];
  return first === undefined ? children : <ExternalLink href={first.url}>{children}</ExternalLink>;
}

function Mattermost({ layout }: { layout: MessageLayout }) {
  const { t } = useTranslation();
  const labels = keyLabelTexts(layout);
  const people = peopleText(t, layout);
  return (
    <div
      className={cn(
        "flex min-w-0 flex-col gap-2 rounded-md border border-l-4 bg-background px-3 py-2 text-sm",
        STATUS_BAR[layout.status.key],
      )}
      data-testid="layout-mattermost"
    >
      <p className="font-semibold wrap-anywhere" data-testid="layout-title">
        {titleLink(layout, titleText(layout))} <span>{`— ${layout.status.word}`}</span>
      </p>
      {layout.fields.length > 0 && (
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 sm:grid-cols-4" data-testid="layout-fields">
          {layout.fields.map((f) => (
            <div key={f.name} className="min-w-0">
              <dt className="text-xs font-semibold text-muted-foreground">{f.name}</dt>
              <dd className="wrap-anywhere">{f.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {summaryTexts(layout).map((text, index) => (
        <p key={index} className="wrap-anywhere">
          {text}
        </p>
      ))}
      {labels.length > 0 && (
        <p className="font-mono text-xs wrap-anywhere" data-testid="layout-labels">
          {labels.join(" · ")}
        </p>
      )}
      {layout.alerts !== null && (
        <p className="wrap-anywhere">
          <AlertsText alerts={layout.alerts} />
        </p>
      )}
      {people !== "" && <p className="wrap-anywhere">{people}</p>}
      {layout.notices.map((notice, index) => (
        <p key={index} className="text-muted-foreground wrap-anywhere">
          {notice}
        </p>
      ))}
      {layout.links.length > 0 && (
        <p className="wrap-anywhere">
          <Links layout={layout} />
        </p>
      )}
    </div>
  );
}

function Block({ kind, children }: { kind: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-start gap-2">
      <span className="mt-0.5 w-14 shrink-0 font-mono text-[10px] text-muted-foreground uppercase">
        {kind}
      </span>
      <div className="min-w-0 flex-1 wrap-anywhere">{children}</div>
    </div>
  );
}

function Slack({ layout }: { layout: MessageLayout }) {
  const { t } = useTranslation();
  const labels = keyLabelTexts(layout);
  const people = peopleText(t, layout);
  return (
    <div
      className="flex min-w-0 flex-col gap-2 rounded-md border bg-background px-3 py-2 text-sm"
      data-testid="layout-slack"
    >
      <Block kind="header">
        <p className="font-semibold" data-testid="layout-title">
          {`${titleText(layout)} — ${layout.status.word}`}
        </p>
      </Block>
      {layout.fields.length > 0 && (
        <Block kind="section">
          <dl className="grid grid-cols-2 gap-x-4 gap-y-1" data-testid="layout-fields">
            {layout.fields.map((f) => (
              <div key={f.name} className="min-w-0">
                <dt className="font-semibold">{f.name}</dt>
                <dd>{f.value}</dd>
              </div>
            ))}
          </dl>
        </Block>
      )}
      {summaryTexts(layout).map((text, index) => (
        <Block key={index} kind="section">
          {text}
        </Block>
      ))}
      {labels.length > 0 && (
        <Block kind="context">
          <span className="font-mono text-xs" data-testid="layout-labels">
            {labels.join(" · ")}
          </span>
        </Block>
      )}
      {layout.alerts !== null && (
        <Block kind="section">
          <AlertsText alerts={layout.alerts} />
        </Block>
      )}
      {people !== "" && <Block kind="context">{people}</Block>}
      {layout.notices.map((notice, index) => (
        <Block key={index} kind="context">
          <span className="text-muted-foreground">{notice}</span>
        </Block>
      ))}
      {layout.links.length > 0 && (
        <Block kind="context">
          <Links layout={layout} />
        </Block>
      )}
    </div>
  );
}

function Telegram({ layout }: { layout: MessageLayout }) {
  const { t } = useTranslation();
  const labels = keyLabelTexts(layout);
  const people = peopleText(t, layout);
  return (
    <div
      className="flex min-w-0 flex-col gap-1 rounded-md border bg-background px-3 py-2 text-sm"
      data-testid="layout-telegram"
    >
      <p className="font-bold wrap-anywhere" data-testid="layout-title">
        {`${titleText(layout)} — ${layout.status.word}`}
      </p>
      {layout.fields.length > 0 && (
        <div className="flex flex-col" data-testid="layout-fields">
          {layout.fields.map((f) => (
            <span key={f.name} className="wrap-anywhere">{`${f.name}: ${f.value}`}</span>
          ))}
        </div>
      )}
      {summaryTexts(layout).map((text, index) => (
        <p key={index} className="wrap-anywhere">
          {text}
        </p>
      ))}
      {labels.length > 0 && (
        <p className="flex flex-wrap gap-x-2 wrap-anywhere" data-testid="layout-labels">
          {labels.map((label) => (
            <code key={label} className="rounded bg-muted px-1 font-mono text-xs">
              {label}
            </code>
          ))}
        </p>
      )}
      {layout.alerts !== null && (
        <p className="wrap-anywhere">
          <AlertsText alerts={layout.alerts} />
        </p>
      )}
      {people !== "" && <p className="wrap-anywhere">{people}</p>}
      {layout.notices.map((notice, index) => (
        <p key={index} className="text-muted-foreground wrap-anywhere">
          {notice}
        </p>
      ))}
      {layout.links.length > 0 && (
        <p className="wrap-anywhere">
          <Links layout={layout} />
        </p>
      )}
    </div>
  );
}

/** The layout of a Root message as Mattermost, Slack or Telegram lays it out, with a note about the buttons. */
export function MessageLayoutPreview({
  layout,
  markup,
}: {
  layout: MessageLayout;
  markup: LayoutMarkup;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex min-w-0 flex-col gap-2" data-testid="message-layout-preview">
      {markup === "markdown" ? (
        <Mattermost layout={layout} />
      ) : markup === "mrkdwn" ? (
        <Slack layout={layout} />
      ) : (
        <Telegram layout={layout} />
      )}
      <p className="text-xs text-muted-foreground">{t("templates.layout.buttonsNote")}</p>
    </div>
  );
}

/**
 * The Bridge tab: the `layout` a bridge receives as JSON beside the CommonMark body. Without a layout (a Route with a
 * template of its own, whose body a bridge receives alone) it says so and shows the body.
 */
export function BridgeLayoutPreview({
  layout,
  body,
}: {
  layout: MessageLayout | null;
  body: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="grid min-w-0 gap-3 md:grid-cols-2" data-testid="message-layout-bridge">
      <div className="flex min-w-0 flex-col gap-1">
        <p className="font-mono text-xs font-medium text-muted-foreground">layout</p>
        {layout === null ? (
          <p className="text-sm text-muted-foreground" data-testid="layout-bridge-none">
            {t("templates.layout.bridgeNone")}
          </p>
        ) : (
          <pre
            className="max-h-96 overflow-auto rounded-md border bg-background p-2 font-mono text-xs leading-5"
            tabIndex={0}
            aria-label={t("templates.layout.bridgeJson")}
            data-testid="layout-json"
          >
            {JSON.stringify(layout, null, 2)}
          </pre>
        )}
      </div>
      <div className="flex min-w-0 flex-col gap-1">
        <p className="font-mono text-xs font-medium text-muted-foreground">body</p>
        <pre
          className="max-h-96 overflow-auto rounded-md border bg-background p-2 font-mono text-xs leading-5 wrap-anywhere whitespace-pre-wrap"
          tabIndex={0}
          aria-label={t("templates.layout.bridgeBody")}
          data-testid="layout-body"
        >
          {body}
        </pre>
      </div>
    </div>
  );
}
