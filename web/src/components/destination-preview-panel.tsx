// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Preview" of a Destination (C-16.FR-4): what it would receive for the chosen Alert Group or the example, rendered by
// previewDestination without sending anything. The Root message of a messenger is drawn in a sandboxed frame with no
// rights at all (sandbox=""): Mattermost Markdown and its attachment from a small subset of Markdown, Telegram HTML from
// the Bot API's subset of tags, both built element by element from text, so that no markup of the output is ever
// parsed into the page; links are shown with their address and are never followed. The buttons are drawn as they
// would appear. An outgoing webhook shows each request — the event, or "Create", "Update", "Open thread" and "Reply in
// thread" — with its method, URL, headers and body; a request whose template fails shows its error.

import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { isValidElement, type ReactNode, useEffect, useId, useMemo } from "react";
import { useTranslation } from "react-i18next";

import { previewDestination } from "../api/gen/endpoints/destinations/destinations";
import type {
  Destination,
  DestinationPreviewItem,
  DestinationPreviewItemName,
} from "../api/gen/model";
import { CodeText, RenderedRequest } from "./rendered-request";
import { isSourceGone, testErrorText, unreachable } from "./destination-test-panel";
import { ExternalLink, inlineMarkdown } from "./template-preview";
import { type PickedSource, TEST_SOURCE_QUERY, testSource } from "./test-source-picker";
import { Button } from "./ui/button";
import { cn } from "./ui/utils";
import { requestName } from "./webhook-destination-fields";

export function previewItemName(t: TFunction, name: DestinationPreviewItemName): string {
  switch (name) {
    case "message":
      return t("destinationPreview.items.message");
    case "event":
      return t("destinationTest.steps.event");
    case "create":
    case "update":
    case "open_thread":
    case "reply_in_thread":
      return requestName(t, name);
    default:
      return unreachable(name);
  }
}

/** The texts the frame shows beside the message itself. */
export interface FrameTexts {
  expandable: string;
}

/** A link of the output as text, with its address after it when that differs from the text: never an anchor. */
function linkNodes(doc: Document, children: Node[], href: string): Node[] {
  const text = doc.createElement("span");
  text.className = "underline underline-offset-2";
  text.append(...children);
  const out: Node[] = [text];
  if (href !== "" && href !== text.textContent) {
    const address = doc.createElement("span");
    address.className = "text-xs text-muted-foreground";
    address.textContent = ` (${href})`;
    out.push(address);
  }
  return out;
}

/** Text with its line breaks as <br> elements. */
function textNodes(doc: Document, text: string): Node[] {
  const out: Node[] = [];
  text.split("\n").forEach((line, index) => {
    if (index > 0) {
      out.push(doc.createElement("br"));
    }
    if (line !== "") {
      out.push(doc.createTextNode(line));
    }
  });
  return out;
}

/** The tags of the Bot API's HTML subset, with the element each one becomes. */
const TELEGRAM_TAGS: Readonly<Record<string, string>> = {
  b: "b",
  strong: "b",
  i: "i",
  em: "i",
  u: "u",
  ins: "u",
  s: "s",
  strike: "s",
  del: "s",
  code: "code",
  pre: "pre",
  blockquote: "blockquote",
};

/** Elements whose content is never shown. */
const DROPPED = new Set(["script", "style", "template", "noscript"]);

/** The block elements of the subset; a line break right after one is part of the block, as in Telegram. */
const BLOCKS = new Set(["blockquote", "pre"]);

function telegramChildren(doc: Document, parent: Node, texts: FrameTexts, pre: boolean): Node[] {
  return Array.from(parent.childNodes).flatMap((node) => {
    const before = node.previousSibling;
    if (
      !pre &&
      node.nodeType === Node.TEXT_NODE &&
      before instanceof Element &&
      BLOCKS.has(before.localName)
    ) {
      return textNodes(doc, (node.textContent ?? "").replace(/^\n/, ""));
    }
    return telegramNode(doc, node, texts, pre);
  });
}

function telegramNode(doc: Document, node: Node, texts: FrameTexts, pre: boolean): Node[] {
  if (node.nodeType === Node.TEXT_NODE) {
    const text = node.textContent ?? "";
    return pre ? [doc.createTextNode(text)] : textNodes(doc, text);
  }
  if (!(node instanceof Element)) {
    return [];
  }
  const tag = node.localName;
  if (DROPPED.has(tag)) {
    return [];
  }
  if (tag === "a") {
    return linkNodes(doc, telegramChildren(doc, node, texts, pre), node.getAttribute("href") ?? "");
  }
  const mapped = TELEGRAM_TAGS[tag];
  if (mapped === undefined) {
    // Spoilers, custom emoji and anything else: their text only.
    return telegramChildren(doc, node, texts, pre);
  }
  const el = doc.createElement(mapped);
  el.append(...telegramChildren(doc, node, texts, pre || mapped === "pre"));
  if (mapped === "blockquote") {
    el.className = "border-l-2 border-primary/60 pl-2";
    if (node.hasAttribute("expandable")) {
      const details = doc.createElement("details");
      details.open = true;
      const summary = doc.createElement("summary");
      summary.className = "cursor-default text-xs text-muted-foreground";
      summary.textContent = texts.expandable;
      details.append(summary, el);
      return [details];
    }
  }
  if (mapped === "pre") {
    el.className = "font-mono text-xs whitespace-pre-wrap";
  }
  return [el];
}

/**
 * Telegram HTML as elements of doc: the HTML is parsed in an inert document, which runs nothing and loads nothing, and
 * only the text and the tags of the Bot API's subset, without any attribute, are copied.
 */
export function telegramNodes(doc: Document, html: string, texts: FrameTexts): Node[] {
  const parsed = new DOMParser().parseFromString(html, "text/html");
  return telegramChildren(doc, parsed.body, texts, false);
}

const MARKDOWN_TAGS = new Set(["strong", "em", "s", "code"]);

/** The elements inlineMarkdown makes, as elements of doc; a link becomes its text and its address. */
// inlineMarkdown makes strings, strong, em, s and code elements and ExternalLink; anything else it might make later
// falls back to its text here, so a change there can never put markup into the frame.
function reactNodes(doc: Document, node: ReactNode): Node[] {
  if (node === null || node === undefined || typeof node === "boolean") {
    return [];
  }
  if (typeof node === "string" || typeof node === "number") {
    return [doc.createTextNode(String(node))];
  }
  if (Array.isArray(node)) {
    return node.flatMap((child: ReactNode) => reactNodes(doc, child));
  }
  if (!isValidElement<{ children?: ReactNode; href?: string }>(node)) {
    return [];
  }
  const children = reactNodes(doc, node.props.children);
  if (node.type === ExternalLink) {
    return linkNodes(doc, children, node.props.href ?? "");
  }
  if (typeof node.type === "string" && MARKDOWN_TAGS.has(node.type)) {
    const el = doc.createElement(node.type);
    if (node.type === "code") {
      el.className = "rounded bg-muted px-1 font-mono text-[0.85em]";
    }
    el.append(...children);
    return [el];
  }
  return children;
}

/** Mattermost Markdown as elements of doc: paragraphs, headings as bold lines, "- " lists and inline Markdown. */
export function markdownNodes(doc: Document, text: string): Node[] {
  const out: Node[] = [];
  let list: HTMLUListElement | null = null;
  for (const line of text.split("\n")) {
    const item = /^\s*[-*] (.*)$/.exec(line);
    if (item !== null) {
      if (list === null) {
        list = doc.createElement("ul");
        list.className = "ml-5 list-disc";
        out.push(list);
      }
      const li = doc.createElement("li");
      li.append(...reactNodes(doc, inlineMarkdown(item[1] ?? "")));
      list.append(li);
      continue;
    }
    list = null;
    if (line.trim() === "") {
      continue;
    }
    const p = doc.createElement("p");
    const heading = /^#{1,6} (.*)$/.exec(line);
    if (heading === null) {
      p.append(...reactNodes(doc, inlineMarkdown(line)));
    } else {
      const b = doc.createElement("b");
      b.append(...reactNodes(doc, inlineMarkdown(heading[1] ?? "")));
      p.append(b);
    }
    out.push(p);
  }
  return out;
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function record(value: unknown): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (value !== null && typeof value === "object" && !Array.isArray(value)) {
    for (const [key, field] of Object.entries(value)) {
      out[key] = field;
    }
  }
  return out;
}

function arrayOf(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

/** The body of a request as JSON, or nothing when it is not an object. */
function jsonBody(body: string | null | undefined): Record<string, unknown> {
  try {
    return record(JSON.parse(body ?? ""));
  } catch {
    return {};
  }
}

function buttonRow(doc: Document, names: string[]): HTMLElement {
  const row = doc.createElement("div");
  row.className = "flex flex-wrap gap-1.5";
  for (const name of names) {
    const button = doc.createElement("button");
    button.type = "button";
    button.disabled = true;
    button.className = "rounded-md border bg-card px-2.5 py-1 text-xs font-medium text-foreground";
    button.textContent = name;
    row.append(button);
  }
  return row;
}

/** The buttons of a Telegram message: the rows of its inline keyboard. */
export function telegramButtons(body: string | null | undefined): string[][] {
  const keyboard = arrayOf(record(jsonBody(body).reply_markup).inline_keyboard);
  return keyboard
    .map((row) => arrayOf(row).map((b) => str(record(b).text)))
    .filter((row) => row.length > 0);
}

/** The attachments of a Mattermost post. */
function attachments(body: string | null | undefined): Record<string, unknown>[] {
  return arrayOf(record(jsonBody(body).props).attachments).map(record);
}

function block(doc: Document, className: string, nodes: Node[]): HTMLElement {
  const div = doc.createElement("div");
  div.className = className;
  div.append(...nodes);
  return div;
}

/** A Mattermost Root message: the post's Markdown, then each attachment with its title, text, fields and buttons. */
export function mattermostNodes(doc: Document, item: DestinationPreviewItem): Node[] {
  const out: Node[] = [block(doc, "flex flex-col gap-1", markdownNodes(doc, item.text ?? ""))];
  for (const a of attachments(item.request?.body)) {
    const parts: Node[] = [];
    if (str(a.pretext) !== "") {
      parts.push(...markdownNodes(doc, str(a.pretext)));
    }
    if (str(a.title) !== "") {
      const title = doc.createElement("p");
      const b = doc.createElement("b");
      b.textContent = str(a.title);
      title.append(b);
      if (str(a.title_link) !== "") {
        title.append(...linkNodes(doc, [], str(a.title_link)).slice(1));
      }
      parts.push(title);
    }
    if (str(a.text) !== "") {
      parts.push(...markdownNodes(doc, str(a.text)));
    }
    const fields = arrayOf(a.fields).map(record);
    if (fields.length > 0) {
      const dl = doc.createElement("dl");
      dl.className = "flex flex-col gap-1";
      for (const f of fields) {
        const dt = doc.createElement("dt");
        dt.className = "font-semibold";
        dt.textContent = str(f.title);
        const dd = doc.createElement("dd");
        dd.append(...markdownNodes(doc, str(f.value)));
        dl.append(dt, dd);
      }
      parts.push(dl);
    }
    const actions = arrayOf(a.actions).map((x) => str(record(x).name));
    if (actions.length > 0) {
      parts.push(buttonRow(doc, actions));
    }
    if (str(a.footer) !== "") {
      const footer = doc.createElement("p");
      footer.className = "text-xs text-muted-foreground";
      footer.textContent = str(a.footer);
      parts.push(footer);
    }
    out.push(block(doc, "flex flex-col gap-1.5 border-l-4 border-primary/60 pl-3", parts));
  }
  return out;
}

/** A Telegram Root message: its HTML, then the rows of its buttons. */
export function telegramMessageNodes(
  doc: Document,
  item: DestinationPreviewItem,
  texts: FrameTexts,
): Node[] {
  const out: Node[] = [block(doc, "", telegramNodes(doc, item.text ?? "", texts))];
  const rows = telegramButtons(item.request?.body);
  if (rows.length > 0) {
    out.push(
      block(
        doc,
        "flex flex-col gap-1.5",
        rows.map((row) => buttonRow(doc, row)),
      ),
    );
  }
  return out;
}

/** The stylesheets of the page, by their own paths, for the frame to look like the page. */
function pageStylesheets(): string[] {
  return Array.from(document.querySelectorAll('link[rel="stylesheet"]'))
    .map((link) => link.getAttribute("href") ?? "")
    .filter((href) => href.startsWith("/") && !href.startsWith("//"));
}

/** The document of the frame: the page's stylesheets and the nodes fill makes, serialized. */
export function frameDocument(
  lang: string,
  stylesheets: string[],
  fill: (doc: Document) => Node[],
): string {
  const doc = document.implementation.createHTMLDocument("");
  doc.documentElement.lang = lang;
  for (const href of stylesheets) {
    const link = doc.createElement("link");
    link.setAttribute("rel", "stylesheet");
    link.setAttribute("href", href);
    doc.head.append(link);
  }
  doc.body.className = "flex flex-col gap-3 p-3 text-sm wrap-anywhere";
  doc.body.append(...fill(doc));
  return `<!doctype html>${doc.documentElement.outerHTML}`;
}

function MessageFrame({ item, title }: { item: DestinationPreviewItem; title: string }) {
  const { t, i18n } = useTranslation();
  const expandable = t("destinationPreview.expandable");
  const lang = i18n.resolvedLanguage ?? "en";
  const html = useMemo(
    () =>
      frameDocument(lang, pageStylesheets(), (doc) =>
        item.format === "html"
          ? telegramMessageNodes(doc, item, { expandable })
          : mattermostNodes(doc, item),
      ),
    [item, lang, expandable],
  );
  return (
    <iframe
      sandbox=""
      srcDoc={html}
      title={title}
      referrerPolicy="no-referrer"
      className="h-80 w-full min-w-0 resize-y rounded-md border bg-background"
      data-testid="preview-frame"
    />
  );
}

function PreviewItem({
  item,
  destinationName,
}: {
  item: DestinationPreviewItem;
  destinationName: string;
}) {
  const { t } = useTranslation();
  const headingId = useId();
  const name = previewItemName(t, item.name);
  const message =
    (item.format === "markdown" || item.format === "html") &&
    item.text !== null &&
    item.text !== undefined;
  let body: ReactNode;
  if (message) {
    body = (
      <>
        <MessageFrame
          item={item}
          title={t("destinationPreview.frameTitle", { destination: destinationName })}
        />
        {item.request !== undefined && (
          <details className="text-sm">
            <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
              {t("destinationTest.request.title")}
            </summary>
            <div className="pt-2">
              <RenderedRequest request={item.request} />
            </div>
          </details>
        )}
      </>
    );
  } else if (item.request !== undefined) {
    body = <RenderedRequest request={item.request} />;
  } else {
    body = (
      <div className="flex min-w-0 flex-col gap-1" data-testid="preview-item-error">
        <p className="text-sm font-medium text-destructive">{t("destinationPreview.failed")}</p>
        <CodeText text={item.text ?? ""} className="text-destructive" />
      </div>
    );
  }
  return (
    <article
      aria-labelledby={headingId}
      className="flex min-w-0 flex-col gap-2 rounded-lg border bg-card p-3"
      data-testid="preview-item"
      data-item={item.name}
    >
      <h3 id={headingId} className="text-sm font-medium">
        {name}
      </h3>
      {body}
    </article>
  );
}

export interface DestinationPreviewPanelProps {
  destination: Destination;
  source: PickedSource;
  /** The chosen Alert Group is no longer offered: the selector reads its list again and returns to the example. */
  onSourceGone: () => void;
}

export function DestinationPreviewPanel({
  destination,
  source,
  onSourceGone,
}: DestinationPreviewPanelProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const request = testSource(source);
  // A new version of the Destination renders again.
  const query = useQuery({
    queryKey: ["previewDestination", destination.id, destination.etag, request],
    queryFn: ({ signal }) => previewDestination(destination.id, { source: request }, { signal }),
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    retry: false,
  });
  const gone = query.isError && isSourceGone(query.error);
  useEffect(() => {
    if (gone) {
      void queryClient.invalidateQueries({ queryKey: [TEST_SOURCE_QUERY] });
      onSourceGone();
    }
  }, [gone, onSourceGone, queryClient]);
  let body: ReactNode;
  if (query.isError) {
    body = (
      <p className="text-sm wrap-anywhere text-destructive" data-testid="preview-error">
        {testErrorText(t, query.error)}
      </p>
    );
  } else if (query.data === undefined) {
    body = <p className="text-sm text-muted-foreground">{t("common.loading")}</p>;
  } else if (query.data.items.length === 0) {
    body = <p className="text-sm text-muted-foreground">{t("destinationPreview.empty")}</p>;
  } else {
    body = query.data.items.map((item, index) => (
      <PreviewItem key={`${item.name}-${index}`} item={item} destinationName={destination.name} />
    ));
  }
  return (
    <div className="flex min-w-0 flex-col gap-3" data-testid="destination-preview">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="min-w-0 text-sm text-muted-foreground">{t("destinationPreview.hint")}</p>
        <Button
          variant="outline"
          size="sm"
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {t("destinationPreview.refresh")}
        </Button>
      </div>
      <div
        className={cn(
          "flex min-w-0 flex-col gap-3 transition-opacity",
          query.isFetching && "opacity-70",
        )}
        aria-busy={query.isFetching}
      >
        {body}
      </div>
      {/* Announces a refusal; the rendered items are read where they are. */}
      <div role="status" aria-live="polite" className="sr-only">
        {query.isError && testErrorText(t, query.error)}
      </div>
    </div>
  );
}
