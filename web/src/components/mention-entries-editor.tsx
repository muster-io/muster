// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Mentions grid of the Destination form (C-28.FR-1, FR-2, FR-10): one row per entry with its target and, under it,
// what it mentions now as the server resolved it; a tick box per Loud event; "Remove"; and "Add" with every target,
// those the Connection cannot mention greyed with their reason. An entry that is stored and whose Connection lost an
// ability stays, greyed, with the reason, and mentions nobody. The notes under the grid say what a Team entry, a person
// without a linked account and the Owner's Reminders do. At 360 px each row stacks: the target, then its tick boxes
// with their names, then "Remove".

import { useTranslation } from "react-i18next";

import { useGetConnection } from "../api/gen/endpoints/connections/connections";
import type { DestinationType, MentionEntry, MentionEvent } from "../api/gen/model";
import {
  MAX_MENTION_ENTRIES,
  MENTION_EVENTS,
  type MentionEntryValue,
  eventName,
  reasonText,
  resolvesToText,
  targetKey,
  targetLabel,
} from "../lib/mention-entries";
import { MentionTargetPicker } from "./mention-target-picker";
import { Button } from "./ui/button";

// The header and the rows share this template, so every header cell sits over its checkbox column; the last column has a
// fixed width because the header and each row are separate grids and an `auto` track would size to its own content.
const GRID =
  "md:grid md:grid-cols-[minmax(11rem,1fr)_repeat(7,minmax(4.5rem,6rem))_6rem] md:items-center md:gap-x-2";

export interface MentionEntriesEditorProps {
  /** The base of the ids, unique on the page. */
  id: string;
  type: DestinationType;
  /** The Destination's Connection; an outgoing webhook has none. */
  connectionId?: string;
  value: readonly MentionEntryValue[];
  onChange: (next: MentionEntryValue[]) => void;
  /** The entries as the server last read them, with what each resolves to now. */
  stored?: readonly MentionEntry[];
  /** Refusals by position of the entry, already translated. */
  errors?: Readonly<Record<number, string>>;
  /** A refusal of the list as a whole, already translated. */
  listError?: string;
  disabled?: boolean;
}

export function MentionEntriesEditor({
  id,
  type,
  connectionId,
  value,
  onChange,
  stored = [],
  errors = {},
  listError,
  disabled = false,
}: MentionEntriesEditorProps) {
  const { t, i18n } = useTranslation();
  const connection = useGetConnection(connectionId ?? "", {
    query: { enabled: connectionId !== undefined && connectionId !== "", retry: false },
  });
  const known = new Map(stored.map((e) => [targetKey(e.target), e]));
  const present = new Set(value.map((v) => targetKey(v.target)));

  const toggle = (index: number, event: MentionEvent, on: boolean) =>
    onChange(
      value.map((v, i) =>
        i === index
          ? {
              ...v,
              events: MENTION_EVENTS.filter((e) => (e === event ? on : v.events.includes(e))),
            }
          : v,
      ),
    );

  return (
    <div className="flex min-w-0 flex-col gap-3" data-testid="mention-entries">
      {value.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-testid="mention-empty">
          {t("mentions.empty")}
        </p>
      ) : (
        <div className="flex min-w-0 flex-col gap-2">
          <div
            className={`hidden border border-transparent px-3 text-xs text-muted-foreground ${GRID}`}
            aria-hidden="true"
            data-testid="mention-header"
          >
            <span>{t("mentions.target")}</span>
            {MENTION_EVENTS.map((event) => (
              <span key={event} className="text-center wrap-anywhere">
                {eventName(t, event)}
              </span>
            ))}
            <span />
          </div>
          <div role="list" aria-label={t("mentions.title")} className="flex min-w-0 flex-col gap-2">
            {value.map((entry, index) => {
              const read = known.get(targetKey(entry.target));
              const label = targetLabel(t, entry.target);
              const reason = read !== undefined && !read.available ? read.unavailable_reason : null;
              const error = errors[index];
              return (
                <div
                  key={`${index}:${targetKey(entry.target)}`}
                  role="listitem"
                  className={`flex min-w-0 flex-col gap-2 rounded-lg border p-3 md:py-2 ${GRID}`}
                  data-testid="mention-entry"
                  data-available={reason === null}
                  data-focus-problem={error === undefined ? undefined : "true"}
                  tabIndex={error === undefined ? undefined : -1}
                >
                  <div
                    className={`flex min-w-0 flex-col gap-0.5 ${reason === null ? "" : "opacity-70"}`}
                  >
                    <span className="font-medium wrap-anywhere" data-testid="mention-entry-target">
                      {label}
                    </span>
                    {reason !== null ? (
                      <span
                        className="text-sm wrap-anywhere text-amber-700 dark:text-amber-300"
                        data-testid="mention-entry-unavailable"
                      >
                        {reasonText(t, reason, type, entry.target.everyone)}
                      </span>
                    ) : (
                      read !== undefined && (
                        <span
                          className="text-sm wrap-anywhere text-muted-foreground"
                          data-testid="mention-entry-now"
                        >
                          {t("mentions.now", { who: resolvesToText(t, i18n.language, read) })}
                        </span>
                      )
                    )}
                  </div>
                  <div className="flex min-w-0 flex-wrap gap-x-4 gap-y-1 md:contents">
                    {MENTION_EVENTS.map((event) => (
                      <label
                        key={event}
                        className="flex items-center gap-2 text-sm md:justify-center"
                      >
                        <input
                          type="checkbox"
                          className="size-4 shrink-0 accent-primary"
                          checked={entry.events.includes(event)}
                          disabled={disabled}
                          aria-label={t("mentions.eventOf", {
                            event: eventName(t, event),
                            target: label,
                          })}
                          data-testid={`mention-event-${event}`}
                          onChange={(e) => toggle(index, event, e.target.checked)}
                        />
                        <span aria-hidden="true" className="md:hidden">
                          {eventName(t, event)}
                        </span>
                      </label>
                    ))}
                  </div>
                  <div className="flex md:justify-end">
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      disabled={disabled}
                      aria-label={t("mentions.removeEntry", { target: label })}
                      onClick={() => onChange(value.filter((_, i) => i !== index))}
                    >
                      {t("mentions.remove")}
                    </Button>
                  </div>
                  {error !== undefined && (
                    <p
                      role="alert"
                      className="text-sm text-destructive md:col-span-full"
                      data-testid="mention-entry-error"
                    >
                      {error}
                    </p>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}
      {listError !== undefined && (
        <p className="text-sm text-destructive" role="alert">
          {listError}
        </p>
      )}
      <MentionTargetPicker
        id={id}
        abilities={{ type, connection: connection.data }}
        connectionId={connectionId}
        present={present}
        full={value.length >= MAX_MENTION_ENTRIES}
        disabled={disabled}
        onAdd={(target) => onChange([...value, { target, events: [...MENTION_EVENTS] }])}
      />
      <ul className="flex flex-col gap-1 text-sm text-muted-foreground" data-testid="mention-notes">
        <li data-testid="mention-note">{t("mentions.notes.team")}</li>
        <li data-testid="mention-note">{t("mentions.notes.unlinked")}</li>
        <li data-testid="mention-note">{t("mentions.notes.always")}</li>
      </ul>
    </div>
  );
}
