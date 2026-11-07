// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Labels in the message" of a Route (C-29.FR-2, C-29.AC-7): the labels of the line of key labels of its Root messages,
// as removable chips in their order. "+ label" opens the suggestions "From recent Alert Groups of the Route", each with
// an example value, and a field for any other name. A Route has the default list, computed from its Group key and the
// Organization's settings, until a person changes it; "Reset to default" goes back to it. Until the Route is saved the
// default list is not known to the form, so it says so instead of showing chips.

import { PlusIcon, XIcon } from "lucide-react";
import { Fragment, type KeyboardEvent, type ReactNode, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { useListRouteLabelSuggestions } from "../api/gen/endpoints/routes/routes";
import type { RouteLabelSuggestion } from "../api/gen/model";
import { fieldErrorText, problemText } from "../lib/api";
import { DocsLink } from "./docs-link";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

/** The most labels a Route lists, as the API checks it. */
export const MESSAGE_LABELS_MAX = 50;

/** The value of the field: the effective list, and whether it is the Route's own. */
export interface MessageLabelsValue {
  own: boolean;
  names: string[];
}

/** Whether two values send the same thing. */
export function sameLabels(a: MessageLabelsValue, b: MessageLabelsValue): boolean {
  if (a.own !== b.own) {
    return false;
  }
  return !a.own || (a.names.length === b.names.length && a.names.every((n, i) => n === b.names[i]));
}

/** Text with `code` spans drawn as code elements. */
function withCode(text: string): ReactNode[] {
  return text.split("`").map((part, index) =>
    index % 2 === 1 ? (
      <code key={index} className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]">
        {part}
      </code>
    ) : (
      <Fragment key={index}>{part}</Fragment>
    ),
  );
}

function Suggestions({
  routeId,
  chosen,
  filter,
  onPick,
  disabled,
}: {
  routeId: string;
  chosen: readonly string[];
  filter: string;
  onPick: (name: string) => void;
  disabled: boolean;
}) {
  const { t } = useTranslation();
  const query = useListRouteLabelSuggestions(routeId, { query: { staleTime: 0 } });
  const needle = filter.trim().toLowerCase();
  const offered: RouteLabelSuggestion[] = (query.data?.items ?? []).filter(
    (s) => !chosen.includes(s.label) && s.label.toLowerCase().includes(needle),
  );
  let body: ReactNode;
  if (query.data === undefined) {
    body = query.isError ? (
      <p className="text-sm text-destructive" role="alert">
        {problemText(t, query.error)}
      </p>
    ) : (
      <p className="text-sm text-muted-foreground" role="status">
        {t("common.loading")}
      </p>
    );
  } else if (offered.length === 0) {
    body = (
      <p className="text-sm text-muted-foreground" data-testid="message-labels-suggest-empty">
        {query.data.items.length === 0
          ? t("routes.messageLabels.noSuggestions")
          : needle !== ""
            ? t("routes.messageLabels.noMatch")
            : t("routes.messageLabels.allAdded")}
      </p>
    );
  } else {
    body = (
      <ul className="flex flex-wrap gap-1.5">
        {offered.map((s) => (
          <li key={s.label} className="max-w-full">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={disabled}
              className="h-auto max-w-full min-w-0 py-1 whitespace-normal"
              data-testid="message-labels-suggestion"
              onClick={() => onPick(s.label)}
            >
              <span className="font-mono wrap-anywhere">
                {t("routes.messageLabels.suggestion", { label: s.label, value: s.example_value })}
              </span>
            </Button>
          </li>
        ))}
      </ul>
    );
  }
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
        {t("routes.messageLabels.suggestions")}
      </p>
      {query.data !== undefined && query.isError && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, query.error)}
        </p>
      )}
      {body}
    </div>
  );
}

export function MessageLabelsEditor({
  id,
  value,
  onChange,
  defaultNames,
  routeId,
  error,
  readOnly = false,
}: {
  id: string;
  value: MessageLabelsValue;
  onChange: (value: MessageLabelsValue) => void;
  /** The computed default list as it was read, or null while it is not known: a new Route, or after an own list was read. */
  defaultNames: readonly string[] | null;
  /** The stored Route, whose recent Alert Groups the suggestions come from; none for a new Route. */
  routeId?: string;
  /** The code of a refusal of the list. */
  error?: string;
  readOnly?: boolean;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const field = useRef<HTMLInputElement>(null);
  const panelId = useId();
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const name = draft.trim();
  const known = value.own || defaultNames !== null;
  // While the list is the default, the names the form read; an own list starts from them when it is first changed.
  const shown = value.own ? value.names : (defaultNames ?? []);
  const full = shown.length >= MESSAGE_LABELS_MAX;
  const duplicate = name !== "" && shown.includes(name);
  const editable = !readOnly && routeId !== undefined;

  const change = (names: string[]) => onChange({ own: true, names });
  const add = (label: string) => {
    const next = label.trim();
    if (next === "" || shown.includes(next) || shown.length >= MESSAGE_LABELS_MAX) {
      return;
    }
    change([...shown, next]);
    setDraft("");
    field.current?.focus();
  };
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      add(draft);
    }
  };
  const message = duplicate
    ? t("routes.messageLabels.duplicate")
    : error === undefined
      ? null
      : fieldErrorText(t, error);

  return (
    <section
      className="flex min-w-0 flex-col gap-2"
      aria-labelledby={`${id}-title`}
      data-testid="message-labels"
    >
      <div className="flex flex-wrap items-baseline gap-x-2">
        <h3 id={`${id}-title`} className="text-sm font-semibold">
          {t("routes.messageLabels.title")}
        </h3>
        <DocsLink
          to="field.message_labels"
          kind="learn_more"
          about={t("docs.fields.messageLabels")}
        />
      </div>
      <p id={hintId} className="text-sm text-muted-foreground">
        {withCode(t("routes.messageLabels.hint"))}
      </p>
      {!known ? (
        <p className="text-sm text-muted-foreground" data-testid="message-labels-unknown">
          {routeId === undefined
            ? t("routes.messageLabels.unknownNew")
            : t("routes.messageLabels.unknownReset")}
        </p>
      ) : shown.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="message-labels-empty">
          {t("routes.messageLabels.empty")}
        </p>
      ) : (
        <ul
          className="flex flex-wrap gap-1.5"
          aria-labelledby={`${id}-title`}
          data-testid="message-labels-list"
        >
          {shown.map((label) => (
            <li
              key={label}
              className="inline-flex max-w-full items-center gap-0.5 rounded-md border bg-muted py-0.5 pr-0.5 pl-2 font-mono text-xs"
              data-testid="message-label"
            >
              <span className={editable ? "min-w-0 wrap-anywhere" : "min-w-0 pr-1.5 wrap-anywhere"}>
                {label}
              </span>
              {editable && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  aria-label={t("routes.messageLabels.remove", { label })}
                  onClick={() => change(shown.filter((l) => l !== label))}
                >
                  <XIcon aria-hidden="true" />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {readOnly && (
        <p className="text-xs text-muted-foreground" data-testid="message-labels-source">
          {value.own ? t("routes.messageLabels.ownList") : t("routes.messageLabels.defaultList")}
        </p>
      )}
      {editable && (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            aria-expanded={open}
            aria-controls={panelId}
            onClick={() => setOpen((o) => !o)}
          >
            <PlusIcon aria-hidden="true" />
            {t("routes.messageLabels.add")}
          </Button>
          {value.own && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                onChange({ own: false, names: [...(defaultNames ?? [])] });
                setOpen(false);
                setDraft("");
              }}
            >
              {t("routes.messageLabels.reset")}
            </Button>
          )}
        </div>
      )}
      {editable && open && (
        <div
          id={panelId}
          className="flex min-w-0 flex-col gap-3 rounded-lg border bg-muted/30 p-3"
          data-testid="message-labels-panel"
        >
          <div className="flex gap-2">
            <Input
              ref={field}
              className="max-w-xs min-w-0 flex-1 font-mono"
              value={draft}
              autoComplete="off"
              spellCheck={false}
              aria-label={t("routes.messageLabels.nameLabel")}
              placeholder={t("routes.messageLabels.placeholder")}
              aria-invalid={message !== null}
              aria-describedby={message === null ? hintId : `${hintId} ${errorId}`}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={onKeyDown}
            />
            <Button
              type="button"
              variant="outline"
              disabled={name === "" || duplicate || full}
              onClick={() => add(draft)}
            >
              {t("routes.messageLabels.addName")}
            </Button>
          </div>
          {full && (
            <p className="text-sm text-muted-foreground">{t("routes.messageLabels.tooMany")}</p>
          )}
          <Suggestions
            routeId={routeId}
            chosen={shown}
            filter={draft}
            disabled={full}
            onPick={add}
          />
        </div>
      )}
      {message !== null && (
        <p id={errorId} className="text-sm break-words text-destructive" role="alert">
          {message}
        </p>
      )}
    </section>
  );
}
