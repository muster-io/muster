// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Request log (C-23.FR-6): the records of a Connection, of a Destination, of an Alert Group's deliveries to one
// Destination or of one check run, newest first and page by page with "Load more" — when each was made in the user's
// time zone, a named arrow for its direction before the operation in a monospace font with the kind of check below it,
// and the status or, without an answer, what failed — an envelope of Slack's Socket Mode, which has no status, as
// confirmed — with the latency below it — filtered by direction, status class
// and operation. Three columns fit 360 px (NFR-15), the date and the time of day on two lines there. The pages keep the
// filters in the URL, so that a link to a filtered list can be shared; the dialogs of the Alert Group page and the
// check panels keep them while they are open. A record opens in a dialog of its own.

import type { TFunction } from "i18next";
import { XIcon } from "lucide-react";
import { type ReactNode, createContext, useContext, useId, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getListRequestLogQueryKey,
  listRequestLog,
} from "../api/gen/endpoints/request-log/request-log";
import {
  type ListRequestLogParams,
  ListRequestLogStatusClass,
  RequestDirection,
  type RequestRecordSummary,
} from "../api/gen/model";
import { SHEET } from "../lib/commands";
import { useTimeFormat } from "../lib/time";
import { checkCodeText } from "./connection-check";
import { type DataColumn, DataTable, useCursorList } from "./data-table";
import {
  PAGE_TABS,
  type PageTab,
  type RequestLogFilters,
  type RequestLogSearch,
} from "./request-log-search";
import {
  DirectionArrow,
  RequestRecordDialog,
  checkKindText,
  errorClassText,
  isEnvelope,
  useRecordTime,
} from "./request-record";
import { StatusTabs } from "./status-tabs";
import { Button } from "./ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";
import { cn } from "./ui/utils";

/** Whose records a list shows: an owner, an Alert Group beside its Destination, or a check run. */
export type RequestLogScope = Pick<
  ListRequestLogParams,
  "connection" | "destination" | "alert_group" | "run"
>;

const DIRECTIONS = Object.values(RequestDirection);
const STATUS_CLASSES = Object.values(ListRequestLogStatusClass);

/**
 * The status of a record, or without an answer what failed: its error code, else its error class; an envelope of
 * Slack's Socket Mode has no status, and Muster confirmed it.
 */
export function statusText(t: TFunction, record: RequestRecordSummary): string {
  if (record.status !== null) {
    return String(record.status);
  }
  if (isEnvelope(record) && record.error_code === null && record.error_class === null) {
    return t("requestLog.envelopeConfirmed");
  }
  if (record.error_code !== null) {
    return checkCodeText(t, record.error_code);
  }
  if (record.error_class !== null) {
    return errorClassText(t, record.error_class);
  }
  return t("requestLog.noAnswer");
}

/** Opens a record; the cells reach it through the context, so that the columns stay the same between renders. */
const OpenRecord = createContext<(record: RequestRecordSummary) => void>(() => undefined);

/**
 * The time of a record as its date, the separator and its time of day, which joined read as the full time: a phone
 * shows the time of day on a line of its own. A language whose full time is not the two joined keeps it whole.
 */
function useRecordTimeParts(): (iso: string) => [string, string, string] {
  const { i18n } = useTranslation();
  const { timeZone } = useTimeFormat();
  const full = useRecordTime();
  const locale = i18n.resolvedLanguage ?? "en";
  return useMemo(() => {
    const date = new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeZone });
    const time = new Intl.DateTimeFormat(locale, {
      timeStyle: "medium",
      hourCycle: "h23",
      timeZone,
    });
    return (iso: string) => {
      const at = new Date(iso);
      const whole = full(iso);
      const [d, t] = [date.format(at), time.format(at)];
      if (whole.length > d.length + t.length && whole.startsWith(d) && whole.endsWith(t)) {
        return [d, whole.slice(d.length, whole.length - t.length), t];
      }
      return [whole, "", ""];
    };
  }, [locale, timeZone, full]);
}

function AtCell({ row }: { row: RequestRecordSummary }) {
  const parts = useRecordTimeParts();
  const [date, separator, time] = parts(row.at);
  return (
    <time dateTime={row.at} className="tabular-nums">
      <span className="whitespace-nowrap">{date}</span>
      <span className="max-sm:hidden">{separator}</span>
      <span className="block whitespace-nowrap sm:inline">{time}</span>
    </time>
  );
}

function OperationCell({ row }: { row: RequestRecordSummary }) {
  const { t } = useTranslation();
  const open = useContext(OpenRecord);
  return (
    <div className="flex min-w-0 items-baseline gap-1.5">
      <DirectionArrow direction={row.direction} />
      <div className="flex min-w-0 flex-col items-start gap-0.5">
        <button
          type="button"
          className="text-left font-mono text-xs wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
          onClick={() => open(row)}
          data-testid="request-operation"
        >
          {row.operation}
        </button>
        {row.check !== null && (
          <span className="text-xs text-muted-foreground" data-testid="request-check">
            {checkKindText(t, row.check)}
          </span>
        )}
      </div>
    </div>
  );
}

function StatusCell({ row }: { row: RequestRecordSummary }) {
  const { t } = useTranslation();
  const failed =
    row.status === null ? row.error_code !== null || row.error_class !== null : row.status >= 400;
  return (
    <div className="flex flex-col items-start gap-0.5">
      <span
        className={cn("tabular-nums", failed && "text-destructive")}
        data-testid="request-status"
      >
        {statusText(t, row)}
      </span>
      <span
        className="text-xs whitespace-nowrap text-muted-foreground tabular-nums"
        data-testid="request-latency"
      >
        {t("requestLog.latency", { ms: row.latency_ms })}
      </span>
    </div>
  );
}

/** The operation, applied on Enter or with "Apply"; the operations of the loaded records are offered. */
function OperationFilter({
  value,
  operations,
  onApply,
}: {
  value: string | undefined;
  operations: readonly string[];
  onApply: (operation: string | undefined) => void;
}) {
  const { t } = useTranslation();
  const id = useId();
  const [text, setText] = useState(value ?? "");
  // A change of the URL (back, a link, "Clear filters") fills the field again.
  const [shown, setShown] = useState(value);
  if (shown !== value) {
    setShown(value);
    setText(value ?? "");
  }
  return (
    <form
      className="flex min-w-0 flex-col gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        const operation = text.trim();
        onApply(operation === "" ? undefined : operation);
      }}
    >
      <Label htmlFor={`${id}-operation`}>{t("requestLog.filters.operation")}</Label>
      <div className="flex min-w-0 gap-2">
        <Input
          id={`${id}-operation`}
          list={`${id}-operations`}
          className="font-mono"
          value={text}
          maxLength={64}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => setText(e.target.value)}
        />
        <datalist id={`${id}-operations`}>
          {operations.map((operation) => (
            <option key={operation} value={operation} />
          ))}
        </datalist>
        <Button type="submit" variant="outline">
          {t("requestLog.filters.apply")}
        </Button>
      </div>
    </form>
  );
}

function Filters({
  filters,
  onChange,
  operations,
  runFilter,
}: {
  filters: RequestLogFilters;
  onChange: (patch: Partial<RequestLogFilters>) => void;
  operations: readonly string[];
  /** Whether the run is a filter of this list, which it shows and clears, or the list's own scope. */
  runFilter: boolean;
}) {
  const { t } = useTranslation();
  const id = useId();
  const run = runFilter ? filters.run : undefined;
  const filtered =
    filters.direction !== undefined ||
    filters.status_class !== undefined ||
    filters.operation !== undefined ||
    run !== undefined;
  return (
    <div className="flex flex-col gap-3" role="search" aria-label={t("requestLog.filters.label")}>
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-direction`}>{t("requestLog.filters.direction")}</Label>
          <NativeSelect
            id={`${id}-direction`}
            className="w-full"
            value={filters.direction ?? ""}
            onChange={(e) => onChange({ direction: DIRECTIONS.find((d) => d === e.target.value) })}
          >
            <NativeSelectOption value="">{t("requestLog.filters.anyDirection")}</NativeSelectOption>
            <NativeSelectOption value="outbound">
              {t("requestLog.direction.outbound")}
            </NativeSelectOption>
            <NativeSelectOption value="inbound">
              {t("requestLog.direction.inbound")}
            </NativeSelectOption>
          </NativeSelect>
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-status`}>{t("requestLog.filters.statusClass")}</Label>
          <NativeSelect
            id={`${id}-status`}
            className="w-full"
            value={filters.status_class ?? ""}
            onChange={(e) =>
              onChange({ status_class: STATUS_CLASSES.find((c) => c === e.target.value) })
            }
          >
            <NativeSelectOption value="">{t("requestLog.filters.anyStatus")}</NativeSelectOption>
            {STATUS_CLASSES.filter((c) => c !== "none").map((c) => (
              <NativeSelectOption key={c} value={c}>
                {c}
              </NativeSelectOption>
            ))}
            <NativeSelectOption value="none">{t("requestLog.noAnswer")}</NativeSelectOption>
          </NativeSelect>
        </div>
        <OperationFilter
          value={filters.operation}
          operations={operations}
          onApply={(operation) => onChange({ operation })}
        />
      </div>
      <div className="flex flex-wrap items-center gap-2 empty:hidden">
        {run !== undefined && (
          <span
            className="inline-flex items-center gap-1 rounded-md border bg-muted/50 py-0.5 pr-0.5 pl-2 text-sm"
            data-testid="request-log-run"
          >
            {t("requestLog.filters.run")}
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={t("requestLog.filters.clearRun")}
              onClick={() => onChange({ run: undefined })}
            >
              <XIcon aria-hidden="true" />
            </Button>
          </span>
        )}
        {filtered && (
          <Button
            variant="link"
            className="px-0"
            onClick={() =>
              onChange({
                direction: undefined,
                status_class: undefined,
                operation: undefined,
                ...(runFilter ? { run: undefined } : {}),
              })
            }
          >
            {t("requestLog.filters.clear")}
          </Button>
        )}
      </div>
    </div>
  );
}

export interface RequestLogProps {
  scope: RequestLogScope;
  filters: RequestLogFilters;
  onFiltersChange: (patch: Partial<RequestLogFilters>) => void;
  /** Called when a link of a record is followed, such as to close the dialog that holds the list. */
  onNavigate?: () => void;
}

export function RequestLog({ scope, filters, onFiltersChange, onNavigate }: RequestLogProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState<RequestRecordSummary | null>(null);
  // Unset filters stay out of the URL and of the query key.
  const params = useMemo(
    (): ListRequestLogParams => ({
      connection: scope.connection,
      destination: scope.destination,
      alert_group: scope.alert_group,
      run: scope.run ?? filters.run,
      direction: filters.direction,
      status_class: filters.status_class,
      operation: filters.operation,
    }),
    [
      scope.connection,
      scope.destination,
      scope.alert_group,
      scope.run,
      filters.run,
      filters.direction,
      filters.status_class,
      filters.operation,
    ],
  );
  const list = useCursorList<RequestRecordSummary>(
    getListRequestLogQueryKey(params),
    (cursor, signal) => listRequestLog({ ...params, cursor }, { signal }),
  );
  const operations = useMemo(
    () => [...new Set(list.items.map((r) => r.operation))].toSorted(),
    [list.items],
  );
  const columns = useMemo(
    (): DataColumn<RequestRecordSummary>[] => [
      // Below 640 px the columns give up their minimum widths and the time its single line, so that the three fit
      // 360 px.
      {
        id: "at",
        header: t("requestLog.columns.at"),
        className: "sm:whitespace-nowrap",
        Cell: AtCell,
      },
      {
        id: "operation",
        header: t("requestLog.columns.operation"),
        className: "sm:min-w-32",
        Cell: OperationCell,
      },
      {
        id: "status",
        header: t("requestLog.columns.status"),
        className: "sm:min-w-24",
        Cell: StatusCell,
      },
    ],
    [t],
  );
  return (
    <OpenRecord value={setOpen}>
      <div className="flex min-w-0 flex-col gap-4" data-testid="request-log">
        <Filters
          filters={filters}
          onChange={onFiltersChange}
          operations={operations}
          runFilter={scope.run === undefined}
        />
        <DataTable
          label={t("requestLog.title")}
          columns={columns}
          list={list}
          rowId={(r) => r.id}
          empty={t("requestLog.empty")}
        />
      </div>
      <RequestRecordDialog
        summary={open}
        onClose={() => setOpen(null)}
        currentRun={params.run}
        onNavigate={onNavigate}
      />
    </OpenRecord>
  );
}

export interface RequestLogButtonProps {
  scope: RequestLogScope;
  /** The text of the button. */
  label: string;
  /** The title of the dialog. */
  title: string;
  className?: string;
}

/** A button that opens the records of its scope in a dialog, whose filters last while it is open. */
export function RequestLogButton({ scope, label, title, className }: RequestLogButtonProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [filters, setFilters] = useState<RequestLogFilters>({});
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className={className}
        onClick={() => {
          setFilters({});
          setOpen(true);
        }}
      >
        {label}
      </Button>
      <Dialog open={open} onOpenChange={(next) => setOpen(next)}>
        <DialogContent
          closeLabel={t("common.close")}
          className={`${SHEET} sm:max-w-3xl`}
          data-testid="request-log-dialog"
        >
          <DialogHeader className="min-w-0">
            <DialogTitle className="pr-8 wrap-anywhere">{title}</DialogTitle>
          </DialogHeader>
          <div className="min-w-0">
            <RequestLog
              scope={scope}
              filters={filters}
              onFiltersChange={(patch) => setFilters((f) => ({ ...f, ...patch }))}
              onNavigate={() => setOpen(false)}
            />
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}

export interface RequestLogTabsProps {
  /** The tab and the filters as the route's search parameters hold them. */
  search: RequestLogSearch;
  onSearch: (patch: Partial<RequestLogSearch>) => void;
  scope: RequestLogScope;
  /** The settings of the page, which stay mounted while the Request log shows, so that unsaved changes last. */
  children: ReactNode;
}

/** The tabs "Settings" and "Request log" of the Connection page and of the outgoing webhook Destination page. */
export function RequestLogTabs({ search, onSearch, scope, children }: RequestLogTabsProps) {
  const { t } = useTranslation();
  const panelId = useId();
  const tab: PageTab = search.tab ?? "settings";
  return (
    <>
      <StatusTabs
        tabs={PAGE_TABS}
        value={tab}
        label={t("requestLog.tabs.label")}
        panelId={panelId}
        labelOf={(x) =>
          x === "settings" ? t("requestLog.tabs.settings") : t("requestLog.tabs.requestLog")
        }
        onSelect={(x) =>
          onSearch(
            x === "settings"
              ? {
                  tab: undefined,
                  direction: undefined,
                  status_class: undefined,
                  operation: undefined,
                  run: undefined,
                }
              : { tab: "request-log" },
          )
        }
      />
      <div
        role="tabpanel"
        id={panelId}
        aria-labelledby={`${panelId}-tab-${tab}`}
        className="min-w-0"
      >
        <div hidden={tab !== "settings"}>
          <div className="flex flex-col gap-6">{children}</div>
        </div>
        {tab === "request-log" && (
          <RequestLog
            scope={scope}
            filters={{
              direction: search.direction,
              status_class: search.status_class,
              operation: search.operation,
              run: search.run,
            }}
            onFiltersChange={onSearch}
          />
        )}
      </div>
    </>
  );
}
