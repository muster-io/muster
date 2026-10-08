// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The editor of a Go template (C-12.FR-2, FR-4, AC-2): a plain textarea over a highlighting layer, with line numbers.
// The textarea keeps the text, the caret, the selection, the keyboard and the screen reader; its text is transparent,
// and the layer under it draws the same text with Go-template highlighting and marks an error at its line and column.
// Nothing is injected into the page that the Content Security Policy (style-src 'self') would refuse: the layer is
// made of elements with classes, and the two scroll together through scrollTop and scrollLeft. Errors come from a
// preview or a refused save (ProblemError with line and column): unknown_function reads "Unknown function: {name}",
// with the name read from the template at the error's position, and template_syntax shows the parser's detail.

import type { TFunction } from "i18next";
import { type ReactNode, useLayoutEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { ProblemError } from "../api/gen/model";
import { fieldErrorText } from "../lib/api";
import { cn } from "./ui/utils";

/** The kinds of a piece of a template, as the layer colours them. */
export type TokenKind =
  | "text"
  | "delimiter"
  | "keyword"
  | "string"
  | "number"
  | "variable"
  | "field"
  | "function"
  | "comment"
  | "punctuation";

export interface Token {
  kind: TokenKind;
  text: string;
}

const KEYWORDS = new Set([
  "if",
  "else",
  "end",
  "range",
  "with",
  "define",
  "template",
  "block",
  "break",
  "continue",
  "nil",
  "true",
  "false",
]);

const IDENT = /[A-Za-z_][A-Za-z0-9_]*/y;
const NUMBER =
  /[-+]?(?:0[xX][0-9a-fA-F_]+|[0-9][0-9_]*(?:\.[0-9_]*)?(?:[eE][-+]?[0-9]+)?|\.[0-9]+)/y;

/** The end of a quoted string that starts at start (a quote), past its closing quote or at the end of the action. */
function quotedEnd(src: string, start: number, limit: number): number {
  const quote = src[start];
  let i = start + 1;
  while (i < limit) {
    const c = src[i];
    if (c === "\\" && quote !== "`") {
      i += 2;
      continue;
    }
    i += 1;
    if (c === quote) {
      return i;
    }
  }
  return limit;
}

/** The kind and the end of the token of an action that starts at i. */
function actionToken(src: string, i: number, to: number): [TokenKind, number] {
  const c = src[i] ?? "";
  if (src.startsWith("/*", i)) {
    const close = src.indexOf("*/", i + 2);
    return ["comment", close < 0 || close + 2 > to ? to : close + 2];
  }
  if (c === '"' || c === "`" || c === "'") {
    return ["string", quotedEnd(src, i, to)];
  }
  if (c === "$") {
    IDENT.lastIndex = i + 1;
    const m = IDENT.exec(src);
    return ["variable", m === null ? i + 1 : i + 1 + m[0].length];
  }
  if (c === ".") {
    IDENT.lastIndex = i + 1;
    const m = IDENT.exec(src);
    if (m !== null) {
      return ["field", i + 1 + m[0].length];
    }
    NUMBER.lastIndex = i;
    const n = NUMBER.exec(src);
    return n === null ? ["field", i + 1] : ["number", i + n[0].length];
  }
  if (/[0-9]/.test(c) || ((c === "-" || c === "+") && /[0-9]/.test(src[i + 1] ?? ""))) {
    NUMBER.lastIndex = i;
    const n = NUMBER.exec(src);
    return ["number", n === null ? i + 1 : i + n[0].length];
  }
  if (/[A-Za-z_]/.test(c)) {
    IDENT.lastIndex = i;
    const word = IDENT.exec(src)?.[0] ?? c;
    return [KEYWORDS.has(word) ? "keyword" : "function", i + word.length];
  }
  return ["|()=:,".includes(c) ? "punctuation" : "text", i + 1];
}

/** The tokens of one action, the text between {{ and }} without the delimiters; runs of plain text are merged. */
function actionTokens(src: string, from: number, to: number, out: Token[]): void {
  for (let i = from; i < to;) {
    const [kind, found] = actionToken(src, i, to);
    const end = Math.min(to, Math.max(found, i + 1));
    const text = src.slice(i, end);
    const last = out[out.length - 1];
    if (kind === "text" && last?.kind === "text") {
      last.text += text;
    } else {
      out.push({ kind, text });
    }
    i = end;
  }
}

/** The text of a template as tokens: text outside actions, and the delimiters and the pieces of each action. */
export function tokenize(src: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  while (i < src.length) {
    const open = src.indexOf("{{", i);
    if (open < 0) {
      out.push({ kind: "text", text: src.slice(i) });
      break;
    }
    if (open > i) {
      out.push({ kind: "text", text: src.slice(i, open) });
    }
    let start = open + 2;
    if (src.startsWith("- ", start)) {
      start += 1;
    }
    out.push({ kind: "delimiter", text: src.slice(open, start) });
    // The action ends at the first }} outside a string or a comment.
    let end = start;
    let close = -1;
    while (end < src.length) {
      const c = src[end];
      if (c === '"' || c === "`" || c === "'") {
        end = quotedEnd(src, end, src.length);
      } else if (src.startsWith("/*", end)) {
        const done = src.indexOf("*/", end + 2);
        end = done < 0 ? src.length : done + 2;
      } else if (src.startsWith("}}", end)) {
        close = end;
        break;
      } else {
        end += 1;
      }
    }
    if (close < 0) {
      actionTokens(src, start, src.length, out);
      break;
    }
    const inner =
      close > start && src[close - 1] === "-" && src[close - 2] === " " ? close - 1 : close;
    actionTokens(src, start, inner, out);
    out.push({ kind: "delimiter", text: src.slice(inner, close + 2) });
    i = close + 2;
  }
  return out;
}

/** The offset (UTF-16 code units) of a 1-based line and column counted in characters, as the server counts them. */
export function offsetOf(src: string, line: number, column: number): number {
  const lines = src.split("\n");
  let offset = 0;
  for (let l = 1; l < line && l <= lines.length; l++) {
    offset += (lines[l - 1] ?? "").length + 1;
  }
  if (line > lines.length) {
    return src.length;
  }
  const chars = Array.from(lines[line - 1] ?? "");
  return offset + chars.slice(0, Math.max(0, column - 1)).join("").length;
}

/** The name at the position of an error, such as the function an unknown_function error is about. */
export function nameAt(src: string, line: number | undefined, column: number | undefined): string {
  if (line === undefined || column === undefined) {
    return "";
  }
  IDENT.lastIndex = offsetOf(src, line, column);
  return IDENT.exec(src)?.[0] ?? "";
}

/** The name of a template, as the Route editor and the Timeline show it. */
export function templateName(t: TFunction, name: string): string {
  switch (name) {
    case "root_message":
      return t("routes.message.rootMessage");
    case "line":
      return t("routes.message.line");
    case "ack_timeout_notice":
      return t("routes.message.ackTimeoutNotice");
    default:
      return name;
  }
}

/** The text of a template error, without its position. */
export function templateErrorText(t: TFunction, error: ProblemError, src: string): string {
  switch (error.code) {
    case "unknown_function": {
      const name = nameAt(src, error.line, error.column);
      return name === ""
        ? (error.detail ?? t("templates.errors.unknownFunctionNoName"))
        : t("templates.errors.unknownFunction", { name });
    }
    case "template_syntax":
      return error.detail ?? t("templates.errors.syntax");
    default:
      return error.detail ?? fieldErrorText(t, error.code);
  }
}

/** The text of a template error with its position: "Line 1, column 4: Unknown function: env". */
export function positionedErrorText(t: TFunction, error: ProblemError, src: string): string {
  const text = templateErrorText(t, error, src);
  if (error.line !== undefined && error.column !== undefined) {
    return t("templates.errors.atLineColumn", {
      line: error.line,
      column: error.column,
      error: text,
    });
  }
  if (error.line !== undefined) {
    return t("templates.errors.atLine", { line: error.line, error: text });
  }
  return text;
}

const TOKEN_CLASS: Record<TokenKind, string> = {
  text: "",
  delimiter: "text-primary font-semibold",
  keyword: "text-violet-700 dark:text-violet-300 font-semibold",
  string: "text-emerald-700 dark:text-emerald-300",
  number: "text-amber-700 dark:text-amber-300",
  variable: "text-rose-700 dark:text-rose-300",
  field: "text-sky-700 dark:text-sky-300",
  function: "text-indigo-700 dark:text-indigo-300",
  comment: "text-muted-foreground italic",
  punctuation: "text-muted-foreground",
};

/** The layer's content: the tokens, with the range of the error marked. */
function layerContent(src: string, mark: [number, number] | null): ReactNode[] {
  const nodes: ReactNode[] = [];
  let at = 0;
  let key = 0;
  const piece = (kind: TokenKind, text: string, marked: boolean) => {
    if (text === "") {
      return;
    }
    key += 1;
    nodes.push(
      <span
        key={key}
        className={cn(
          TOKEN_CLASS[kind],
          marked &&
            "rounded-sm bg-destructive/15 underline decoration-destructive decoration-wavy underline-offset-4",
        )}
        data-error={marked ? "true" : undefined}
      >
        {text}
      </span>,
    );
  };
  for (const token of tokenize(src)) {
    const start = at;
    const end = at + token.text.length;
    at = end;
    if (mark === null || end <= mark[0] || start >= mark[1]) {
      piece(token.kind, token.text, false);
      continue;
    }
    const from = Math.max(start, mark[0]) - start;
    const to = Math.min(end, mark[1]) - start;
    piece(token.kind, token.text.slice(0, from), false);
    piece(token.kind, token.text.slice(from, to), true);
    piece(token.kind, token.text.slice(to), false);
  }
  if (mark !== null && mark[0] >= src.length) {
    // An error past the end of the text, such as an action that is never closed.
    piece("text", " ", true);
  }
  return nodes;
}

/** The range of the text an error marks: the name at its position, or one character. */
function markOf(src: string, error: ProblemError | undefined): [number, number] | null {
  if (error?.line === undefined) {
    return null;
  }
  const start = offsetOf(src, error.line, error.column ?? 1);
  const name = error.column === undefined ? "" : nameAt(src, error.line, error.column);
  if (name !== "") {
    return [start, start + name.length];
  }
  const char = Array.from(src.slice(start))[0] ?? "";
  return [start, start + Math.max(1, char.length)];
}

export interface TemplateEditorProps {
  id: string;
  value: string;
  onChange?: (value: string) => void;
  /** The errors of the last preview or refused save; the first one with a position is marked in the text. */
  errors?: readonly ProblemError[];
  /** The accessible name of the editor. */
  label: string;
  /** Ids of hints that describe the editor besides its errors. */
  describedBy?: string;
  readOnly?: boolean;
  disabled?: boolean;
  minRows?: number;
  maxRows?: number;
  placeholder?: string;
  /** Receives the textarea, such as the ref of a form field that the form focuses on an error. */
  inputRef?: (element: HTMLTextAreaElement | null) => void;
}

export function TemplateEditor({
  id,
  value,
  onChange,
  errors = [],
  label,
  describedBy,
  readOnly = false,
  disabled = false,
  minRows = 3,
  maxRows = 16,
  placeholder,
  inputRef,
}: TemplateEditorProps) {
  const { t } = useTranslation();
  const area = useRef<HTMLTextAreaElement>(null);
  const layer = useRef<HTMLPreElement>(null);
  const gutter = useRef<HTMLDivElement>(null);
  const lines = value.split("\n");
  const marked = errors.find((e) => e.line !== undefined) ?? errors[0];
  const mark = markOf(value, marked);
  const errorLine = marked?.line;
  const errorId = `${id}-errors`;
  const described =
    [describedBy, errors.length > 0 ? errorId : undefined].filter(Boolean).join(" ") || undefined;

  const sync = () => {
    const a = area.current;
    if (a === null) {
      return;
    }
    if (layer.current !== null) {
      layer.current.scrollTop = a.scrollTop;
      layer.current.scrollLeft = a.scrollLeft;
    }
    if (gutter.current !== null) {
      gutter.current.scrollTop = a.scrollTop;
    }
  };
  useLayoutEffect(sync);

  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <div
        className={cn(
          "flex min-w-0 overflow-hidden rounded-lg border border-input bg-transparent font-mono text-sm leading-6 focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/50 dark:bg-input/30",
          errors.length > 0 && "border-destructive focus-within:ring-destructive/20",
          (readOnly || disabled) && "bg-muted/40",
        )}
        data-testid="template-editor"
      >
        {/* The numbers scroll with the text; the hidden copy of the last number gives the column its width. */}
        <div
          aria-hidden="true"
          className="relative shrink-0 border-r bg-muted/40 text-right text-xs leading-6 text-muted-foreground tabular-nums select-none"
        >
          <div className="invisible h-0 overflow-hidden pr-1.5 pl-2">{lines.length}</div>
          <div ref={gutter} className="absolute inset-0 overflow-hidden py-1.5 pr-1.5 pl-2">
            {lines.map((_, index) => (
              <div
                key={index}
                className={cn(index + 1 === errorLine && "font-semibold text-destructive")}
                data-error-line={index + 1 === errorLine ? "true" : undefined}
              >
                {index + 1}
              </div>
            ))}
          </div>
        </div>
        <div className="relative min-w-0 flex-1">
          <pre
            ref={layer}
            aria-hidden="true"
            className="pointer-events-none absolute inset-0 m-0 overflow-hidden py-1.5 pr-6 pl-2 font-mono text-sm leading-6 whitespace-pre text-foreground forced-colors:hidden"
            data-testid="template-layer"
          >
            {layerContent(value, mark)}
            {"\n"}
          </pre>
          <textarea
            ref={(element) => {
              area.current = element;
              inputRef?.(element);
            }}
            id={id}
            value={value}
            readOnly={readOnly}
            disabled={disabled}
            wrap="off"
            spellCheck={false}
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            placeholder={placeholder}
            rows={Math.min(maxRows, Math.max(minRows, lines.length))}
            aria-label={label}
            aria-invalid={errors.length > 0}
            aria-describedby={described}
            className="relative block w-full min-w-0 resize-y overflow-auto bg-transparent px-2 py-1.5 font-mono text-sm leading-6 whitespace-pre text-transparent caret-foreground outline-none forced-colors:text-[CanvasText] selection:bg-primary/25 placeholder:text-muted-foreground disabled:cursor-not-allowed"
            onScroll={sync}
            onChange={(e) => onChange?.(e.target.value)}
          />
        </div>
      </div>
      {errors.length > 0 && (
        <ul id={errorId} className="flex flex-col gap-0.5" data-testid="template-errors">
          {errors.map((error, index) => (
            <li key={index} className="text-sm break-words whitespace-pre-wrap text-destructive">
              {positionedErrorText(t, error, value)}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
