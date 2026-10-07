// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Alert Group list, the home page (C-09.FR-13, FR-24, FR-25): status tabs with counts, filters in a side panel on a
// desktop and a sheet on a phone, time range, search by #N or text, sort, label columns and "Load more", all in the
// URL. Live hints keep it current without moving it: a changed Alert Group updates its row in place, and Alert Groups
// that newly match are announced as "N new" above the list and shown when that is pressed. After a reconnect of the
// stream everything is read again. "Mine" filters the Alert Groups the user owns (C-10.FR-13); "Select" puts
// checkboxes on the rows for a bulk command (C-10.FR-14), the selection kept across "Load more" and dropped when the
// view changes.

import {
  type InfiniteData,
  keepPreviousData,
  useInfiniteQuery,
  useIsMutating,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { SlidersHorizontalIcon } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getAlertGroup,
  getAlertGroupCounts,
  getGetAlertGroupCountsQueryKey,
  getListAlertGroupsQueryKey,
  listAlertGroups,
} from "../api/gen/endpoints/alert-groups/alert-groups";
import {
  type AlertGroup,
  type AlertGroupCounts,
  type AlertGroupList,
  AgSortParameter,
} from "../api/gen/model";
import { AlertGroupFilters } from "../components/alert-group-filters";
import {
  AlertGroupTable,
  type Selection,
  SelectionContext,
  useWideLayout,
} from "../components/alert-group-table";
import { RequirePermission, useCan } from "../components/app-shell";
import { BulkActionsBar } from "../components/bulk-actions-bar";
import { personName } from "../components/command-buttons";
import type { CursorList } from "../components/data-table";
import { NewAlertGroupsBanner } from "../components/new-alert-groups-banner";
import { MineToggle } from "../components/owner-filter";
import { StatusTabs } from "../components/status-tabs";
import { TimeRangePicker } from "../components/time-range-picker";
import { Button } from "../components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import {
  ALERT_GROUP_TABS,
  type AlertGroupSearch,
  type AlertGroupTab,
  DEFAULT_SORT,
  activeFilterCount,
  alertGroupSearchSchema,
  compact,
  countParams,
  listParams,
} from "../lib/alert-group-search";
import { BULK_COMMAND_KEY, putListRow } from "../lib/commands";
import { onHint } from "../lib/live";

export const Route = createFileRoute("/alert-groups/")({
  validateSearch: alertGroupSearchSchema,
  staticData: { shell: true },
  component: AlertGroupsPage,
});

/** How long the search waits for typing to pause before it filters. */
const SEARCH_DELAY_MS = 300;

function tabLabel(t: TFunction, tab: AlertGroupTab): string {
  switch (tab) {
    case "open":
      return t("alertGroups.tabs.open");
    case "firing":
      return t("alertGroups.tabs.firing");
    case "acknowledged":
      return t("alertGroups.tabs.acknowledged");
    case "snoozed":
      return t("alertGroups.tabs.snoozed");
    case "resolved":
      return t("alertGroups.tabs.resolved");
    default:
      return t("alertGroups.tabs.all");
  }
}

/** The count of a tab; Open is the sum of the three open statuses. */
function tabCount(counts: AlertGroupCounts | undefined, tab: AlertGroupTab): number | undefined {
  if (counts === undefined) {
    return undefined;
  }
  switch (tab) {
    case "open":
      return counts.firing + counts.acknowledged + counts.snoozed;
    default:
      return counts[tab];
  }
}

function sortLabel(t: TFunction, sort: AgSortParameter): string {
  switch (sort) {
    case "-started_at":
      return t("alertGroups.sort.startedDesc");
    case "started_at":
      return t("alertGroups.sort.startedAsc");
    case "-last_changed_at":
      return t("alertGroups.sort.lastChangeDesc");
    default:
      return t("alertGroups.sort.lastChangeAsc");
  }
}

/** The search field: a #N or text, applied once typing pauses. */
function SearchField({ value, onSearch }: { value: string; onSearch: (q: string) => void }) {
  const { t } = useTranslation();
  const [text, setText] = useState(value);
  const [previous, setPrevious] = useState(value);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const sent = useRef(value);
  // The URL changed elsewhere (back, a link): the field follows it, unless it is what was typed...
  if (value !== previous) {
    setPrevious(value);
    if (value !== text.trim()) {
      setText(value);
    }
  }
  // ...and a search still waiting for a pause in typing is dropped, so that it does not undo that change.
  useEffect(() => {
    if (value !== sent.current) {
      clearTimeout(timer.current);
      sent.current = value;
    }
  }, [value]);
  useEffect(() => () => clearTimeout(timer.current), []);
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Label htmlFor="alert-groups-q">{t("alertGroups.search.label")}</Label>
      <Input
        id="alert-groups-q"
        type="search"
        value={text}
        autoComplete="off"
        placeholder={t("alertGroups.search.placeholder")}
        onChange={(e) => {
          const next = e.target.value;
          setText(next);
          clearTimeout(timer.current);
          timer.current = setTimeout(() => {
            sent.current = next.trim();
            onSearch(next.trim());
          }, SEARCH_DELAY_MS);
        }}
      />
    </div>
  );
}

/**
 * Whether an Alert Group comes before the last shown row in the order of the list, so that it belongs to the rows
 * already loaded; one that comes after it is on a page not loaded yet, and is not new.
 */
function sortsBefore(
  group: AlertGroup,
  last: AlertGroup | undefined,
  sort: AgSortParameter | undefined,
): boolean {
  if (last === undefined) {
    return true;
  }
  const field = (sort ?? DEFAULT_SORT).replace(/^-/, "");
  const at = (g: AlertGroup) =>
    Date.parse(field === "last_changed_at" ? g.last_changed_at : g.started_at);
  return (sort ?? DEFAULT_SORT).startsWith("-") ? at(group) >= at(last) : at(group) <= at(last);
}

/** The list for the view, its pages kept still between hints, and "N new" for what newly matches. */
function useLiveList(search: AlertGroupSearch, at: number, enabled: boolean) {
  const queryClient = useQueryClient();
  const params = useMemo(() => compact(listParams(search, at)), [search, at]);
  const baseKey = getListAlertGroupsQueryKey(params);
  const pagesKey = useMemo(() => [...getListAlertGroupsQueryKey(params), "pages"], [params]);
  const query = useInfiniteQuery<
    AlertGroupList,
    Error,
    InfiniteData<AlertGroupList, string | undefined>,
    readonly unknown[],
    string | undefined
  >({
    queryKey: pagesKey,
    queryFn: ({ pageParam, signal }) =>
      listAlertGroups({ ...params, cursor: pageParam }, { signal }),
    initialPageParam: undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    placeholderData: keepPreviousData,
    // The rows move only when the user asks: focus and a network that comes back read nothing.
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    enabled,
  });
  const items = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data]);
  const [pending, setPending] = useState<{ key: string; ids: string[] }>({ key: "", ids: [] });
  const keyText = JSON.stringify(baseKey);

  // The hint listeners read the current view through a ref, as they outlive renders.
  const current = useRef({
    params,
    pagesKey,
    keyText,
    shown: new Set<string>(),
    last: undefined as AlertGroup | undefined,
    hasMore: false,
    placeholder: false,
  });
  useEffect(() => {
    current.current = {
      params,
      pagesKey,
      keyText,
      shown: new Set(items.map((g) => g.id)),
      last: items.at(-1),
      hasMore: query.hasNextPage,
      placeholder: query.isPlaceholderData,
    };
  });
  // Each read of a hint has a number; an answer older than the last read of the same thing is dropped, so that a
  // read that started before a second change never overwrites it.
  const reads = useRef(new Map<string, number>());

  useEffect(() => {
    if (!enabled) {
      return undefined;
    }
    // The first page as it is now; what it has that the list does not show is new.
    const numbers = reads.current;
    const nextRead = (name: string) => {
      const n = (numbers.get(name) ?? 0) + 1;
      numbers.set(name, n);
      return () => numbers.get(name) === n;
    };
    const probe = () => {
      const { params: p, keyText: k, placeholder } = current.current;
      // The rows of the previous filters still show: there is nothing to compare with yet.
      if (placeholder) {
        return;
      }
      const latest = nextRead(`head:${k}`);
      void listAlertGroups(p)
        .then((head) => {
          const now = current.current;
          if (now.keyText !== k || !latest()) {
            return;
          }
          const fresh = head.items
            .filter(
              (g) => !now.shown.has(g.id) && (!now.hasMore || sortsBefore(g, now.last, p.sort)),
            )
            .map((g) => g.id);
          setPending((prev) =>
            prev.key === k && prev.ids.join() === fresh.join() ? prev : { key: k, ids: fresh },
          );
        })
        .catch(() => {
          // The list shows its own errors; a failed probe announces nothing.
        });
    };
    // A shown Alert Group changed: its row is read again and replaced in place. Another one may now match the view.
    const refreshRow = (id: string | null) => {
      if (id === null || !current.current.shown.has(id)) {
        probe();
        return;
      }
      const key = current.current.pagesKey;
      const latest = nextRead(`row:${id}`);
      void getAlertGroup(id)
        .then((fresh) => {
          if (!latest()) {
            return;
          }
          queryClient.setQueryData<InfiniteData<AlertGroupList, string | undefined>>(key, (data) =>
            data === undefined
              ? data
              : {
                  ...data,
                  pages: data.pages.map((page) => ({
                    ...page,
                    items: page.items.map((g): AlertGroup =>
                      g.id === id ? { ...fresh, label_values: g.label_values } : g,
                    ),
                  })),
                },
          );
        })
        .catch(() => {
          // A row that cannot be read stays as it was.
        });
    };
    const stopNew = onHint("alert-groups", probe);
    const stopChanged = onHint("alert-group", refreshRow);
    return () => {
      stopNew();
      stopChanged();
    };
  }, [enabled, queryClient]);

  const shown = new Set(items.map((g) => g.id));
  const fresh = pending.key === keyText ? pending.ids.filter((id) => !shown.has(id)) : [];
  const list: CursorList<AlertGroup> = {
    items,
    hasMore: query.hasNextPage,
    loadMore: () => {
      void query.fetchNextPage();
    },
    isLoading: query.isPending && enabled,
    isLoadingMore: query.isFetchingNextPage,
    error: query.error,
  };
  return {
    list,
    newCount: fresh.length,
    // Shows the new Alert Groups: the list is read again from its first page, keeping the rows until it arrives.
    showNew: () => {
      setPending({ key: keyText, ids: [] });
      void queryClient.invalidateQueries({ queryKey: pagesKey });
    },
  };
}

/** The selection of the list: kept across "Load more", dropped when the view changes. */
function useSelection(viewKey: string) {
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set());
  const [view, setView] = useState(viewKey);
  if (view !== viewKey) {
    setView(viewKey);
    setSelected(new Set());
  }
  const selection: Selection = {
    selecting,
    selected,
    toggle: (id) =>
      setSelected((prev) => {
        const next = new Set(prev);
        if (next.has(id)) {
          next.delete(id);
        } else {
          next.add(id);
        }
        return next;
      }),
  };
  return {
    selection,
    setSelected,
    start: () => setSelecting(true),
    stop: () => {
      setSelecting(false);
      setSelected(new Set());
    },
  };
}

function AlertGroups() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const wide = useWideLayout();
  const [sheet, setSheet] = useState(false);
  const panel = useRef<HTMLDivElement>(null);
  // The presets of the time range count back from when the page opened or the range was chosen; the list then holds
  // still instead of moving its start on every read.
  const [at, setAt] = useState(() => Date.now());
  const tab = search.tab ?? "open";
  const update = (patch: Partial<AlertGroupSearch>) => {
    if ("range" in patch) {
      setAt(Date.now());
    }
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  };

  // A custom range that does not start before it ends is not sent.
  const badRange =
    search.range === "custom" &&
    search.from !== undefined &&
    search.to !== undefined &&
    Date.parse(search.from) >= Date.parse(search.to);
  const { list, newCount, showNew } = useLiveList(search, at, !badRange);
  const countsParams = useMemo(() => compact(countParams(search, at)), [search, at]);
  const counts = useQuery({
    queryKey: getGetAlertGroupCountsQueryKey(countsParams),
    queryFn: ({ signal }) => getAlertGroupCounts(countsParams, { signal }),
    placeholderData: keepPreviousData,
    refetchOnWindowFocus: false,
    enabled: !badRange,
  });

  const filtered = activeFilterCount(search) > 0 || Boolean(search.q);
  const empty = filtered ? t("alertGroups.emptyFiltered") : t("alertGroups.empty");
  const filters = (
    <AlertGroupFilters search={search} onChange={update} problem={list.error} showColumns={wide} />
  );
  const panelId = "alert-groups-panel";
  const active = activeFilterCount(search);
  const canAck = useCan("alert-groups:acknowledge");
  const canResolve = useCan("alert-groups:resolve");
  const canSnooze = useCan("alert-groups:snooze");
  const canBulk = canAck || canResolve || canSnooze;
  const { selection, setSelected, start, stop } = useSelection(JSON.stringify(search));
  const shownIds = list.items.map((g) => g.id);
  // Only rows the user sees are sent: one that left the list (after "N new" read it again) is no longer selected.
  const selectedIds = shownIds.filter((id) => selection.selected.has(id));
  const bulkBusy = useIsMutating({ mutationKey: BULK_COMMAND_KEY }) > 0;
  const allShown = shownIds.length > 0 && shownIds.every((id) => selection.selected.has(id));
  const owners = new Map(
    list.items.flatMap((g) => (g.owner === undefined ? [] : [[g.id, personName(t, g.owner)]])),
  );
  // After a bulk command each Alert Group it ran on is read again and replaced in place, as a live hint would.
  const refreshRows = (ids: readonly string[]) => {
    setSelected(new Set());
    void queryClient.invalidateQueries({ queryKey: getGetAlertGroupCountsQueryKey() });
    for (const id of ids) {
      void getAlertGroup(id)
        .then((fresh) => putListRow(queryClient, fresh))
        .catch(() => {
          // A row that cannot be read stays as it was.
        });
    }
  };
  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-2xl font-semibold tracking-tight">{t("alertGroups.title")}</h1>
      <StatusTabs
        tabs={ALERT_GROUP_TABS}
        value={tab}
        label={t("alertGroups.tabs.label")}
        panelId={panelId}
        labelOf={(x) => tabLabel(t, x)}
        countOf={(x) => tabCount(counts.data, x)}
        onSelect={(next) => update({ tab: next === "open" ? undefined : next })}
      />
      <div
        className="grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-[2fr_1fr_1fr]"
        role="search"
        aria-label={t("alertGroups.search.region")}
      >
        <SearchField
          value={search.q ?? ""}
          onSearch={(q) => update({ q: q === "" ? undefined : q })}
        />
        <div className="flex min-w-0 flex-col gap-1">
          <TimeRangePicker search={search} onChange={update} />
          {badRange && (
            <p className="text-sm text-destructive" role="alert">
              {t("alertGroups.range.invalid")}
            </p>
          )}
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor="alert-groups-sort">{t("alertGroups.sort.label")}</Label>
          <NativeSelect
            id="alert-groups-sort"
            className="w-full"
            value={search.sort ?? DEFAULT_SORT}
            onChange={(e) => {
              const sort = Object.values(AgSortParameter).find((s) => s === e.target.value);
              update({ sort: sort === DEFAULT_SORT ? undefined : sort });
            }}
          >
            {(["-started_at", "started_at", "-last_changed_at", "last_changed_at"] as const).map(
              (s) => (
                <NativeSelectOption key={s} value={s}>
                  {sortLabel(t, s)}
                </NativeSelectOption>
              ),
            )}
          </NativeSelect>
        </div>
      </div>
      {!wide && (
        <div>
          <Button
            variant="outline"
            onClick={() => setSheet(true)}
            aria-haspopup="dialog"
            data-testid="filters-button"
          >
            <SlidersHorizontalIcon aria-hidden="true" />
            {active > 0
              ? t("alertGroups.filters.buttonActive", { count: active })
              : t("alertGroups.filters.button")}
          </Button>
          <Dialog open={sheet} onOpenChange={setSheet}>
            <DialogContent
              closeLabel={t("common.close")}
              className="top-0 right-0 bottom-0 left-auto flex h-dvh max-h-dvh w-full max-w-[min(24rem,100%)] translate-x-0 translate-y-0 flex-col gap-4 overflow-y-auto rounded-none sm:max-w-sm"
            >
              <DialogHeader>
                <DialogTitle>{t("alertGroups.filters.title")}</DialogTitle>
              </DialogHeader>
              {filters}
              <Button onClick={() => setSheet(false)}>{t("alertGroups.filters.done")}</Button>
            </DialogContent>
          </Dialog>
        </div>
      )}
      <div className="flex min-w-0 flex-col gap-4 lg:flex-row lg:items-start">
        {wide && (
          <aside
            className="flex w-72 shrink-0 flex-col gap-3 rounded-lg border p-3"
            aria-label={t("alertGroups.filters.title")}
          >
            <h2 className="text-sm font-semibold">{t("alertGroups.filters.title")}</h2>
            {filters}
          </aside>
        )}
        <div
          className="flex min-w-0 flex-1 flex-col gap-3 outline-none"
          role="tabpanel"
          ref={panel}
          tabIndex={-1}
          id={panelId}
          aria-labelledby={`${panelId}-tab-${tab}`}
        >
          <div className="flex flex-wrap items-center gap-2">
            <MineToggle value={search.owner} onChange={(owner) => update({ owner })} />
            {canBulk && (
              <Button
                variant="outline"
                aria-pressed={selection.selecting}
                disabled={bulkBusy}
                onClick={selection.selecting ? stop : start}
                data-testid="select-button"
              >
                {selection.selecting ? t("commands.bulk.stop") : t("commands.bulk.select")}
              </Button>
            )}
          </div>
          <NewAlertGroupsBanner
            count={newCount}
            onShow={() => {
              showNew();
              // The button goes away: the focus moves to the list it filled.
              panel.current?.focus();
            }}
          />
          <SelectionContext value={selection}>
            <AlertGroupTable
              list={list}
              labelColumns={search.columns ?? []}
              empty={empty}
              wide={wide}
            />
          </SelectionContext>
          {selection.selecting && (
            <BulkActionsBar
              ids={selectedIds}
              owners={owners}
              allShown={allShown}
              onToggleAllShown={() => setSelected(allShown ? new Set() : new Set(shownIds))}
              onClear={() => setSelected(new Set())}
              onDone={refreshRows}
            />
          )}
        </div>
      </div>
    </div>
  );
}

function AlertGroupsPage() {
  return (
    <RequirePermission permission="alert-groups:read">
      <AlertGroups />
    </RequirePermission>
  );
}
