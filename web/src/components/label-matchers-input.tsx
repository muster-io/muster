// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Matcher input of the label filters (C-06.FR-19, reused by the Alert Group list): a list of Matchers in the
// Alertmanager syntax (name="value", !=, =~, !~), each a chip, combined with AND and sent as repeated `label`. The
// syntax is checked as typed; whatever passes it goes to the server, whose Problem for `label` (a regular expression
// that does not compile, say) shows under the field while the typed text and the applied filters stay.

import type { TFunction } from "i18next";
import { XIcon } from "lucide-react";
import { type FormEvent, useState } from "react";
import { useTranslation } from "react-i18next";

import { fieldErrorText, isApiError, problemText } from "../lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/** Why a typed Matcher is not in the Alertmanager syntax. */
export type MatcherSyntaxError =
  | "name"
  | "unclosed_name"
  | "operator"
  | "unclosed_value"
  | "after_value"
  | "quote_in_value";

const OPERATORS = ["=~", "!~", "!=", "="] as const;

/** The index of the quote that closes the one at the start of s, or -1; a backslash escapes the next character. */
function closingQuote(s: string): number {
  for (let i = 1; i < s.length; i++) {
    if (s[i] === "\\") {
      i++;
    } else if (s[i] === '"') {
      return i;
    }
  }
  return -1;
}

/** Trims white space as Go's unicode.IsSpace sees it, which also counts U+0085. */
function trimSpace(s: string): string {
  return s.replace(/^[\s\u0085]+|[\s\u0085]+$/gu, "");
}

const NAME_START = /^[\p{L}_:]/u;
const NAME_PART = /^[\p{L}\p{Nd}_:]*/u;

/**
 * Checks the syntax of one Matcher as the server parses it: a label name, plain or quoted; an operator; a value,
 * quoted (and then last) or unquoted without quotes. The server also compiles the regular expressions and checks the
 * escapes of quoted strings, and answers with a Problem when they are wrong.
 */
export function checkMatcher(text: string): MatcherSyntaxError | null {
  const s = trimSpace(text);
  let rest: string;
  if (s.startsWith('"')) {
    const end = closingQuote(s);
    if (end < 0) {
      return "unclosed_name";
    }
    if (end === 1) {
      return "name";
    }
    rest = s.slice(end + 1);
  } else {
    if (!NAME_START.test(s)) {
      return "name";
    }
    rest = s.slice(1 + (NAME_PART.exec(s.slice(1))?.[0].length ?? 0));
  }
  rest = rest.replace(/^[\s\u0085]+/u, "");
  const op = OPERATORS.find((o) => rest.startsWith(o));
  if (op === undefined) {
    return "operator";
  }
  const value = trimSpace(rest.slice(op.length));
  if (!value.startsWith('"')) {
    return value.includes('"') ? "quote_in_value" : null;
  }
  const end = closingQuote(value);
  if (end < 0) {
    return "unclosed_value";
  }
  return trimSpace(value.slice(end + 1)) === "" ? null : "after_value";
}

function syntaxText(t: TFunction, error: MatcherSyntaxError): string {
  switch (error) {
    case "name":
      return t("matchers.syntax.name");
    case "unclosed_name":
      return t("matchers.syntax.unclosedName");
    case "operator":
      return t("matchers.syntax.operator");
    case "unclosed_value":
      return t("matchers.syntax.unclosedValue");
    case "after_value":
      return t("matchers.syntax.afterValue");
    default:
      return t("matchers.syntax.quoteInValue");
  }
}

/** The error the server gave for the Matcher at index of `label`: its code and its explanation. */
export interface MatcherProblem {
  index: number;
  code: string;
  detail?: string;
}

const LABEL_POINTER = /^\/query\/label(?:\/(\d+))?$/;

/** The Problem of a request for its `label` parameter, when it has one. */
export function matcherProblem(err: unknown): MatcherProblem | undefined {
  if (!isApiError(err)) {
    return undefined;
  }
  for (const item of err.errors ?? []) {
    const match = LABEL_POINTER.exec(item.pointer);
    if (match) {
      return { index: Number(match[1] ?? 0), code: item.code, detail: item.detail };
    }
  }
  return undefined;
}

/** The text of the server's error for a Matcher. */
export function matcherProblemText(t: TFunction, problem: MatcherProblem): string {
  switch (problem.code) {
    case "invalid_regex":
      return problem.detail
        ? t("matchers.errors.invalidRegexDetail", { detail: problem.detail })
        : t("matchers.errors.invalidRegex");
    case "invalid_format":
      return t("matchers.errors.invalidFormat");
    default:
      return fieldErrorText(t, problem.code);
  }
}

export interface LabelMatchersInputProps {
  /** The id of the text field; the label, hint and errors hang off it. */
  id: string;
  /** The applied Matchers. */
  value: readonly string[];
  /**
   * Applies a new list of Matchers. When it rejects, the field shows the error, the typed text stays and so do the
   * applied filters: a Problem for `label` names the Matcher the server refused.
   */
  onChange: (next: string[]) => Promise<void> | void;
  /** The error of the list that the applied Matchers filter, shown under the field when it is about them. */
  problem?: unknown;
}

export function LabelMatchersInput({ id, value, onChange, problem }: LabelMatchersInputProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState("");
  const [refusal, setRefusal] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const syntax = trimSpace(draft) === "" ? null : checkMatcher(draft);
  const duplicate = value.includes(trimSpace(draft));
  const applied = matcherProblem(problem);
  const appliedText =
    applied !== undefined && value[applied.index] !== undefined
      ? t("matchers.errors.applied", {
          matcher: value[applied.index],
          error: matcherProblemText(t, applied),
        })
      : null;

  const apply = async (next: string[], typed: string | null) => {
    setBusy(true);
    setRefusal(null);
    try {
      await onChange(next);
      if (typed !== null) {
        setDraft("");
      }
    } catch (err) {
      const found = matcherProblem(err);
      setRefusal(found === undefined ? problemText(t, err) : matcherProblemText(t, found));
    } finally {
      setBusy(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const typed = trimSpace(draft);
    if (typed === "" || syntax !== null || duplicate || busy) {
      return;
    }
    void apply([...value, typed], typed);
  };

  // The syntax check follows every keystroke, so it is not announced; the server's refusal is, once.
  const syntaxMessage =
    syntax === null ? (duplicate ? t("matchers.syntax.duplicate") : null) : syntaxText(t, syntax);
  const message = refusal ?? syntaxMessage ?? appliedText;
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor={id}>{t("matchers.label")}</Label>
      {value.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label={t("matchers.applied")}>
          {value.map((matcher, index) => (
            <li
              key={matcher}
              className="inline-flex max-w-full items-center gap-0.5 rounded-md border bg-muted py-0.5 pr-0.5 pl-2 font-mono text-xs"
              data-testid="matcher-chip"
            >
              <span className="min-w-0 wrap-anywhere">{matcher}</span>
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={t("matchers.remove", { matcher })}
                disabled={busy}
                onClick={() =>
                  void apply(
                    value.filter((_, i) => i !== index),
                    null,
                  )
                }
              >
                <XIcon aria-hidden="true" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <form className="flex gap-2" onSubmit={submit}>
        <Input
          id={id}
          className="min-w-0 flex-1 font-mono"
          value={draft}
          autoComplete="off"
          spellCheck={false}
          placeholder={'namespace="payments"'}
          aria-invalid={message !== null}
          aria-describedby={`${hintId} ${errorId}`}
          onChange={(e) => {
            setDraft(e.target.value);
            setRefusal(null);
          }}
        />
        <Button
          type="submit"
          variant="outline"
          disabled={trimSpace(draft) === "" || syntax !== null || duplicate || busy}
        >
          {t("matchers.add")}
        </Button>
      </form>
      <p id={hintId} className="text-xs text-muted-foreground">
        {t("matchers.hint")}
      </p>
      <div id={errorId} className="empty:hidden">
        {message !== null && (
          <p
            className="text-sm break-words text-destructive"
            role={refusal === null ? undefined : "alert"}
            data-testid="matcher-error"
          >
            {message}
          </p>
        )}
      </div>
    </div>
  );
}
