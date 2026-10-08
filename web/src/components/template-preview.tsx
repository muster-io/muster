// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The live preview of a template (C-12.FR-5, AC-6): previewTemplate with the template a short while after the text
// stops changing, rendered against the chosen sample, in Mattermost Markdown or Telegram HTML. The output is untrusted:
// a Mattermost result is drawn from a small subset of Markdown into elements, never as HTML, and links open only when
// they are http(s), in a new tab; a Telegram result is shown as its HTML text. A template that fails shows its error in
// place of the message, and the editor marks it.

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { previewTemplate } from "../api/gen/endpoints/templates/templates";
import type {
  Language,
  TemplateKind,
  TemplatePreviewRequest,
  TemplatePreviewResult,
} from "../api/gen/model";
import { problemText } from "../lib/api";
import { type Sample, sampleFields } from "./sample-picker";
import { positionedErrorText } from "./template-editor";
import { cn } from "./ui/utils";

/** The markup of a preview: Mattermost Markdown or Telegram HTML. */
export type PreviewFormat = "markdown" | "html";

/** How long the preview waits after the last change of the text before it renders. */
export const PREVIEW_DELAY_MS = 400;

/**
 * A value that follows another once it has stopped changing for delay milliseconds; key tells the values apart, so that
 * a new object with the same content does not start the wait again.
 */
export function useDebounced<T>(value: T, key: string, delay: number): T {
  const [settled, setSettled] = useState({ key, value });
  const latest = useRef(value);
  useEffect(() => {
    latest.current = value;
  });
  useEffect(() => {
    if (key === settled.key) {
      return undefined;
    }
    const timer = setTimeout(() => setSettled({ key, value: latest.current }), delay);
    return () => clearTimeout(timer);
  }, [key, settled.key, delay]);
  return settled.value;
}

export interface PreviewParams {
  kind: TemplateKind;
  /** The template; empty renders the built-in one. */
  template: string;
  routeId?: string;
  language?: Language;
  sample: Sample;
  format?: PreviewFormat;
}

/** The request of a preview. */
export function previewRequest(p: PreviewParams): TemplatePreviewRequest {
  return {
    kind: p.kind,
    template: p.template,
    format: p.format ?? null,
    route_id: p.routeId ?? null,
    language: p.language ?? null,
    ...sampleFields(p.sample),
  };
}

/** The preview of a template, rendered a short while after its parameters stop changing. */
export function useTemplatePreview(params: PreviewParams, enabled = true) {
  const current = previewRequest(params);
  const request = useDebounced(current, JSON.stringify(current), PREVIEW_DELAY_MS);
  const query = useQuery({
    queryKey: ["previewTemplate", request],
    queryFn: ({ signal }) => previewTemplate(request, { signal }),
    enabled,
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    retry: false,
  });
  // The errors of a result point into the template it rendered; an older result kept on screen names none.
  return { query, template: query.isPlaceholderData ? undefined : request.template };
}

/** A preview with the template its result belongs to. */
export type TemplatePreviewState = ReturnType<typeof useTemplatePreview>;

/** The errors of a preview for the template the editor holds, or none while the preview is for another text. */
export function previewErrors(preview: TemplatePreviewState, template: string) {
  const data = preview.query.data;
  return data !== undefined && !data.valid && preview.template === template ? data.errors : [];
}

/** Whether a URL may be opened from a preview or the Links block: absolute http(s) only. */
export function safeUrl(url: string): string | undefined {
  try {
    const parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}

/** Tells screen readers that a link opens in a new tab. */
function NewTabHint() {
  const { t } = useTranslation();
  return <span className="sr-only">{` ${t("links.newTab")}`}</span>;
}

/** A link of untrusted output: opened in a new tab without access to this page, or plain text when not http(s). */
export function ExternalLink({
  href,
  children,
  className,
}: {
  href: string;
  children: ReactNode;
  className?: string;
}) {
  const url = safeUrl(href);
  if (url === undefined) {
    return <span className={className}>{children}</span>;
  }
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className={cn(
        "relative text-primary underline underline-offset-4 wrap-anywhere hover:text-primary/80",
        className,
      )}
    >
      {children}
      <NewTabHint />
    </a>
  );
}

/** The inline markers of the Markdown subset, with the element each one makes. */
const MARKERS: readonly {
  open: string;
  close: string;
  tag: "strong" | "em" | "s" | "code";
  /** Opens and closes only at the edge of a word, as in snake_case_names, which stay text. */
  edges?: boolean;
}[] = [
  { open: "**", close: "**", tag: "strong" },
  { open: "~~", close: "~~", tag: "s" },
  { open: "`", close: "`", tag: "code" },
  { open: "_", close: "_", tag: "em", edges: true },
  { open: "*", close: "*", tag: "em", edges: true },
];

const WORD = /[\p{L}\p{N}]/u;

function isWord(c: string | undefined): boolean {
  return c !== undefined && WORD.test(c);
}

const LINK = /^\[((?:\\.|[^\]\\])*)\]\(([^()\s]*)\)/;

/** Removes the backslash escapes of Markdown. */
function unescape(text: string): string {
  return text.replace(/\\([\\`*_{}[\]()#+\-.!~|>])/g, "$1");
}

/**
 * The inline Markdown of one line as elements: backslash escapes, **bold**, _italic_ and *italic*, ~~struck~~, `code`
 * and [text](url). Anything else is text.
 */
export function inlineMarkdown(text: string, key = "i"): ReactNode[] {
  const out: ReactNode[] = [];
  let plain = "";
  let n = 0;
  const flush = () => {
    if (plain !== "") {
      out.push(plain);
      plain = "";
    }
  };
  let i = 0;
  while (i < text.length) {
    const c = text[i] ?? "";
    if (c === "\\" && i + 1 < text.length) {
      plain += text[i + 1];
      i += 2;
      continue;
    }
    if (c === "[") {
      const m = LINK.exec(text.slice(i));
      if (m !== null) {
        flush();
        n += 1;
        out.push(
          <ExternalLink key={`${key}-${n}`} href={m[2] ?? ""}>
            {inlineMarkdown(m[1] ?? "", `${key}-${n}`)}
          </ExternalLink>,
        );
        i += m[0].length;
        continue;
      }
    }
    const marker = MARKERS.find(
      (m) =>
        text.startsWith(m.open, i) &&
        !(m.edges === true && (isWord(text[i - 1]) || /\s/.test(text[i + 1] ?? " "))),
    );
    if (marker !== undefined) {
      const close = findClose(text, i + marker.open.length, marker.close, marker.edges === true);
      if (close > i + marker.open.length) {
        flush();
        n += 1;
        const inner = text.slice(i + marker.open.length, close);
        const Tag = marker.tag;
        out.push(
          <Tag
            key={`${key}-${n}`}
            className={
              Tag === "code" ? "rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]" : undefined
            }
          >
            {Tag === "code" ? unescape(inner) : inlineMarkdown(inner, `${key}-${n}`)}
          </Tag>,
        );
        i = close + marker.close.length;
        continue;
      }
    }
    plain += c;
    i += 1;
  }
  flush();
  return out;
}

/** The position of a closing marker after from that is not escaped (and ends a word, with edges), or -1. */
function findClose(text: string, from: number, close: string, edges: boolean): number {
  for (let i = from; i < text.length; i++) {
    if (text[i] === "\\") {
      i += 1;
      continue;
    }
    if (text.startsWith(close, i) && !(edges && isWord(text[i + close.length]))) {
      return i;
    }
  }
  return -1;
}

/** Mattermost Markdown as elements: lines, "- " lists and inline Markdown; raw HTML stays text. */
export function MarkdownText({ text }: { text: string }) {
  const blocks: ReactNode[] = [];
  let list: string[] = [];
  const flushList = () => {
    if (list.length > 0) {
      const items = list;
      blocks.push(
        <ul key={`l${blocks.length}`} className="ml-5 list-disc">
          {items.map((item, index) => (
            <li key={index}>{inlineMarkdown(item, `l${blocks.length}-${index}`)}</li>
          ))}
        </ul>,
      );
      list = [];
    }
  };
  for (const line of text.split("\n")) {
    const item = /^\s*[-*] (.*)$/.exec(line);
    if (item !== null) {
      list.push(item[1] ?? "");
      continue;
    }
    flushList();
    if (line.trim() !== "") {
      blocks.push(<p key={`p${blocks.length}`}>{inlineMarkdown(line, `p${blocks.length}`)}</p>);
    }
  }
  flushList();
  return <div className="flex flex-col gap-1 text-sm wrap-anywhere">{blocks}</div>;
}

/** The radio pair that switches a preview between Mattermost and Telegram markup. */
export function FormatSwitch({
  name,
  value,
  onChange,
}: {
  name: string;
  value: PreviewFormat;
  onChange: (format: PreviewFormat) => void;
}) {
  const { t } = useTranslation();
  const options: [PreviewFormat, string][] = [
    ["markdown", t("templates.preview.mattermost")],
    ["html", t("templates.preview.telegram")],
  ];
  return (
    <fieldset className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <legend className="sr-only">{t("templates.preview.markup")}</legend>
      {options.map(([format, label]) => (
        <label key={format} className="flex items-center gap-1.5 text-sm">
          <input
            type="radio"
            name={name}
            value={format}
            checked={value === format}
            className="size-4 accent-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            onChange={() => onChange(format)}
          />
          {label}
        </label>
      ))}
    </fieldset>
  );
}

/** The rendered output of a preview. */
function Output({ result, kind }: { result: TemplatePreviewResult; kind: TemplateKind }) {
  const { t } = useTranslation();
  const output = result.output ?? "";
  if (kind === "link_rule") {
    return output === "" ? (
      <p className="text-sm text-muted-foreground">{t("templates.preview.noLink")}</p>
    ) : (
      <p className="font-mono text-sm wrap-anywhere" data-testid="template-preview-output">
        <ExternalLink href={output}>{output}</ExternalLink>
      </p>
    );
  }
  if (output === "") {
    return <p className="text-sm text-muted-foreground">{t("templates.preview.empty")}</p>;
  }
  if (result.format === "html") {
    return (
      <pre
        className="font-mono text-xs leading-5 wrap-anywhere whitespace-pre-wrap"
        data-testid="template-preview-output"
      >
        {output}
      </pre>
    );
  }
  return (
    <div
      className="rounded-md border-l-4 border-primary/60 bg-background px-3 py-2"
      data-testid="template-preview-output"
    >
      <MarkdownText text={output} />
    </div>
  );
}

export function TemplatePreview({
  id,
  kind,
  preview,
}: {
  id: string;
  kind: TemplateKind;
  preview: TemplatePreviewState;
}) {
  const { t } = useTranslation();
  const { query } = preview;
  // The names that errors point at are read from the template the result belongs to.
  const template = preview.template ?? "";
  const result = query.data;
  let body: ReactNode;
  if (query.isError) {
    body = (
      <p className="text-sm text-destructive" role="alert">
        {problemText(t, query.error)}
      </p>
    );
  } else if (result === undefined) {
    body = (
      <p className="text-sm text-muted-foreground" role="status">
        {t("common.loading")}
      </p>
    );
  } else if (!result.valid) {
    body = (
      <div className="flex flex-col gap-1" data-testid="template-preview-error">
        <p className="text-sm font-medium text-destructive">{t("templates.preview.failed")}</p>
        {result.errors.map((error, index) => (
          <p key={index} className="text-sm break-words whitespace-pre-wrap text-destructive">
            {positionedErrorText(t, error, template)}
          </p>
        ))}
      </div>
    );
  } else {
    body = (
      <div className="flex flex-col gap-2">
        <Output result={result} kind={kind} />
        {result.truncated === true && (
          <p className="text-xs text-muted-foreground" data-testid="template-preview-truncated">
            {t("templates.preview.truncated")}
          </p>
        )}
        {result.sample === "example" && (
          <p className="text-xs text-muted-foreground">{t("templates.preview.example")}</p>
        )}
      </div>
    );
  }
  return (
    <div
      id={id}
      className={cn(
        "flex min-w-0 flex-col gap-2 rounded-lg border bg-muted/30 p-3 transition-opacity",
        query.isFetching && "opacity-70",
      )}
      aria-busy={query.isFetching}
      data-testid="template-preview"
    >
      <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
        {t("templates.preview.title")}
      </p>
      <div aria-live="polite">{body}</div>
    </div>
  );
}
