// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The shared table of the list pages (ADR-0009): TanStack Table renders the rows, TanStack Query reads the pages of a
// cursor-paginated list (next_cursor, never an offset) and "Load more" fetches the next one. The pages keep their
// filters as typed search parameters in the URL and pass them in the query key.

import {
  type InfiniteData,
  type QueryKey,
  keepPreviousData,
  useInfiniteQuery,
} from "@tanstack/react-query";
import { type ColumnDef, type RowData, tableFeatures, useTable } from "@tanstack/react-table";
import { type ComponentType, type ReactNode, useMemo } from "react";
import { useTranslation } from "react-i18next";

import { problemText } from "../lib/api";
import { Button } from "./ui/button";
import { cn } from "./ui/utils";

/** A page of a list as the API returns it. */
export interface CursorPage<T> {
  items: T[];
  next_cursor: string | null;
}

export interface CursorList<T> {
  items: T[];
  hasMore: boolean;
  loadMore: () => void;
  isLoading: boolean;
  isLoadingMore: boolean;
  error: unknown;
}

/** Reads a cursor-paginated list page by page; a new key (other filters) starts again from the first page. */
export function useCursorList<T>(
  queryKey: QueryKey,
  fetchPage: (cursor: string | undefined, signal: AbortSignal) => Promise<CursorPage<T>>,
  enabled = true,
): CursorList<T> {
  const query = useInfiniteQuery<
    CursorPage<T>,
    Error,
    InfiniteData<CursorPage<T>, string | undefined>,
    QueryKey,
    string | undefined
  >({
    queryKey: [...queryKey, "pages"],
    queryFn: ({ pageParam, signal }) => fetchPage(pageParam, signal),
    initialPageParam: undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    placeholderData: keepPreviousData,
    enabled,
  });
  const items = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data]);
  return {
    items,
    hasMore: query.hasNextPage,
    loadMore: () => {
      void query.fetchNextPage();
    },
    isLoading: query.isPending,
    isLoadingMore: query.isFetchingNextPage,
    error: query.error,
  };
}

/** A column of a DataTable: its header and how a row shows in it, as text or through a component. */
export type DataColumn<T> = {
  id: string;
  header: string;
  /** Classes of the header and the cells, such as a minimum width. */
  className?: string;
} & ({ text: (row: T) => string } | { Cell: ComponentType<{ row: T }> });

const features = tableFeatures({});

function cellOf<T>(column: DataColumn<T>, row: T): ReactNode {
  if ("text" in column) {
    return column.text(row);
  }
  const { Cell } = column;
  return <Cell row={row} />;
}

export interface DataTableProps<T extends RowData> {
  /** The accessible name of the table. */
  label: string;
  columns: readonly DataColumn<T>[];
  list: CursorList<T>;
  rowId: (row: T) => string;
  /** The text when the list is empty. */
  empty: string;
}

export function DataTable<T extends RowData>({
  label,
  columns,
  list,
  rowId,
  empty,
}: DataTableProps<T>) {
  const { t } = useTranslation();
  const defs = useMemo(
    () =>
      columns.map((c): ColumnDef<typeof features, T> => ({
        id: c.id,
        header: c.header,
        cell: (ctx) => cellOf(c, ctx.row.original),
      })),
    [columns],
  );
  const classes = useMemo(() => new Map(columns.map((c) => [c.id, c.className])), [columns]);
  const table = useTable({
    features,
    columns: defs,
    data: list.items,
    getRowId: (row) => rowId(row),
  });
  const rows = table.getRowModel().rows;
  return (
    <div className="flex flex-col gap-3">
      {/* Wide tables scroll inside their own box, so the page never scrolls sideways; the box also contains the
          positioned texts for screen readers. */}
      <div className="relative overflow-x-auto rounded-lg border">
        <table className="w-full border-collapse text-left text-sm" aria-label={label}>
          <thead className="bg-muted/50">
            {table.getHeaderGroups().map((group) => (
              <tr key={group.id}>
                {group.headers.map((header) => (
                  <th
                    key={header.id}
                    scope="col"
                    className={cn(
                      "px-3 py-2 align-bottom font-medium whitespace-nowrap text-muted-foreground",
                      classes.get(header.column.id),
                    )}
                  >
                    {header.isPlaceholder ? null : <table.FlexRender header={header} />}
                  </th>
                ))}
              </tr>
            ))}
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id} className="border-t align-top">
                {row.getAllCells().map((cell) => (
                  <td key={cell.id} className={cn("px-3 py-2", classes.get(cell.column.id))}>
                    <table.FlexRender cell={cell} />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        {rows.length === 0 && (
          <p className="px-3 py-6 text-center text-sm text-muted-foreground" role="status">
            {list.isLoading ? t("common.loading") : list.error ? problemText(t, list.error) : empty}
          </p>
        )}
      </div>
      {rows.length > 0 && list.error !== null && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, list.error)}
        </p>
      )}
      {list.hasMore && (
        <div>
          <Button variant="outline" disabled={list.isLoadingMore} onClick={list.loadMore}>
            {list.isLoadingMore ? t("common.loading") : t("table.loadMore")}
          </Button>
        </div>
      )}
    </div>
  );
}
