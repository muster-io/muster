// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Routes in evaluation order (C-08.FR-3): each with its name, its Matchers in Alertmanager syntax, the urgent mark,
// the Group key and the count of open Alert Groups; the Default route pinned last. With routes:write a Route moves by
// dragging its handle (the browser's own drag and drop, which adds nothing to the page that the Content Security
// Policy would refuse) or with "Move up" and "Move down", which also serve the keyboard and touch screens. Every move
// saves the whole order with If-Match from the list ETag; a save over a newer order (412) shows "Someone else changed
// the order. Reload to see it." and puts the order back as it was read.

import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowDownIcon, ArrowUpIcon, GripVerticalIcon } from "lucide-react";
import { type DragEvent, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getListRoutesQueryKey,
  getListRoutesUrl,
  getReorderRoutesUrl,
} from "../api/gen/endpoints/routes/routes";
import type { Route, RouteList as RouteListModel, RouteOrder } from "../api/gen/model";
import { type Tagged, apiFetchTagged, isApiError, isStale, problemText } from "../lib/api";
import { matcherText } from "./matcher-builder";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { cn } from "./ui/utils";

/** The Routes list with its ETag, which covers the order. A route hint invalidates it through the list's key. */
export const routesQuery = queryOptions({
  queryKey: [...getListRoutesQueryKey(), "tagged"],
  queryFn: ({ signal }) =>
    apiFetchTagged<RouteListModel>(getListRoutesUrl(), { method: "GET", signal }),
});

function reorder(order: RouteOrder, etag: string): Promise<Tagged<RouteListModel>> {
  return apiFetchTagged<RouteListModel>(getReorderRoutesUrl(), {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": etag },
    body: JSON.stringify(order),
  });
}

/** The order with the Route id placed before or after target; a target of null puts it last. */
export function moveTo(
  ids: readonly string[],
  id: string,
  target: string | null,
  before: boolean,
): string[] {
  const rest = ids.filter((i) => i !== id);
  const at = target === null ? -1 : rest.indexOf(target);
  if (at < 0) {
    return [...rest, id];
  }
  rest.splice(before ? at : at + 1, 0, id);
  return rest;
}

/** The order with the Route id moved one place up (-1) or down (1). */
export function moveBy(ids: readonly string[], id: string, step: -1 | 1): string[] {
  const from = ids.indexOf(id);
  const to = from + step;
  if (from < 0 || to < 0 || to >= ids.length) {
    return [...ids];
  }
  const next = [...ids];
  next.splice(from, 1);
  next.splice(to, 0, id);
  return next;
}

function sameOrder(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((id, i) => id === b[i]);
}

/** Where a dragged Route would land: before or after the Route under the pointer. */
interface DropTarget {
  id: string;
  before: boolean;
}

function RouteRow({
  route,
  number,
  canMove,
  first,
  last,
  busy,
  dropTarget,
  onDragStart,
  onDragEnd,
  onMove,
  dragProps,
}: {
  route: Route;
  number: number;
  canMove: boolean;
  first: boolean;
  last: boolean;
  /** A save of the order is under way: the moves wait for it. */
  busy: boolean;
  dropTarget: DropTarget | null;
  onDragStart: (e: DragEvent<HTMLElement>, row: HTMLLIElement | null) => void;
  onDragEnd: () => void;
  onMove: (step: -1 | 1) => void;
  dragProps: {
    onDragOver: (e: DragEvent<HTMLLIElement>) => void;
    onDrop: (e: DragEvent<HTMLLIElement>) => void;
  };
}) {
  const { t } = useTranslation();
  const row = useRef<HTMLLIElement>(null);
  const indicator =
    dropTarget?.id === route.id
      ? dropTarget.before
        ? "shadow-[inset_0_3px_0_0_var(--color-primary)]"
        : "shadow-[inset_0_-3px_0_0_var(--color-primary)]"
      : undefined;
  return (
    <li
      ref={row}
      className={cn("flex items-start gap-2 border-t px-3 py-3 first:border-t-0", indicator)}
      data-testid="route-row"
      data-route-id={route.id}
      data-default={route.is_default ? "true" : undefined}
      {...dragProps}
    >
      {canMove && (
        <div
          draggable
          aria-hidden="true"
          title={t("routes.list.dragHint")}
          className="mt-0.5 -ml-1 flex size-6 shrink-0 cursor-grab items-center justify-center rounded text-muted-foreground hover:bg-muted active:cursor-grabbing"
          data-testid="drag-handle"
          onDragStart={(e) => onDragStart(e, row.current)}
          onDragEnd={onDragEnd}
        >
          <GripVerticalIcon className="size-4" />
        </div>
      )}
      <span
        aria-hidden="true"
        className="mt-0.5 w-5 shrink-0 text-right text-sm text-muted-foreground tabular-nums"
      >
        {route.is_default ? "" : `${number}.`}
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <Link
            to="/routes/$routeId"
            params={{ routeId: route.id }}
            className="min-w-0 font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
            data-route-link={route.id}
          >
            {route.name}
          </Link>
          {route.urgent && (
            <span
              className="inline-flex items-center rounded-md border border-destructive/40 px-1.5 py-0.5 text-xs font-medium text-destructive"
              data-testid="route-urgent"
            >
              {t("routes.list.urgent")}
            </span>
          )}
        </div>
        {route.is_default ? (
          <p className="text-sm text-muted-foreground">{t("routes.list.defaultHint")}</p>
        ) : (
          <ul
            className="flex flex-wrap gap-1"
            aria-label={t("routes.fields.matchers")}
            data-testid="route-matchers"
          >
            {route.matchers.length === 0 ? (
              <li className="text-sm text-muted-foreground">{t("routes.list.noMatchers")}</li>
            ) : (
              route.matchers.map((m) => (
                <li
                  key={matcherText(m)}
                  className="max-w-full rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs wrap-anywhere"
                >
                  {matcherText(m)}
                </li>
              ))
            )}
          </ul>
        )}
        <p className="text-sm text-muted-foreground" data-testid="route-group-key">
          <span>{t("routes.fields.groupKey")}: </span>
          {route.group_key.length === 0 ? (
            t("routes.list.noGroupKey")
          ) : (
            <span className="font-mono text-xs wrap-anywhere text-foreground">
              {route.group_key.join(", ")}
            </span>
          )}
        </p>
        <p className="text-sm text-muted-foreground" data-testid="route-open-count">
          {t("routes.list.openAlertGroups", { count: route.open_alert_group_count })}
        </p>
      </div>
      {canMove && (
        <div className="flex shrink-0 flex-col gap-1 sm:flex-row">
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="aria-disabled:cursor-not-allowed aria-disabled:opacity-40 aria-disabled:hover:bg-transparent"
            aria-label={t("routes.list.moveUp", { name: route.name })}
            aria-disabled={first || busy}
            data-move={`${route.id}-up`}
            onClick={() => {
              if (!first && !busy) {
                onMove(-1);
              }
            }}
          >
            <ArrowUpIcon aria-hidden="true" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="aria-disabled:cursor-not-allowed aria-disabled:opacity-40 aria-disabled:hover:bg-transparent"
            aria-label={t("routes.list.moveDown", { name: route.name })}
            aria-disabled={last || busy}
            data-move={`${route.id}-down`}
            onClick={() => {
              if (!last && !busy) {
                onMove(1);
              }
            }}
          >
            <ArrowDownIcon aria-hidden="true" />
          </Button>
        </div>
      )}
    </li>
  );
}

export function RouteList({
  canWrite,
  focusRouteId,
  onFocused,
}: {
  canWrite: boolean;
  /** A Route whose link takes the focus once the list shows it, such as the Route an accepted suggestion created. */
  focusRouteId?: string;
  onFocused?: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const query = useQuery(routesQuery);
  const [pending, setPending] = useState<string[] | null>(null);
  const [stale, setStale] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const [dragging, setDragging] = useState<string | null>(null);
  const [dropTarget, setDropTarget] = useState<DropTarget | null>(null);
  // The move button to focus again, once the list shows the order it moved to.
  const focusMove = useRef<{ button: string; order: string } | null>(null);

  const save = useMutation({
    mutationFn: async ({ ids, etag }: { ids: string[]; etag: string }) => {
      // A read of the list still under way (a live hint) must not land after the saved order and bring the old one
      // back.
      await queryClient.cancelQueries({ queryKey: routesQuery.queryKey });
      return reorder({ route_ids: ids }, etag);
    },
    onSuccess: (list) => {
      queryClient.setQueryData(routesQuery.queryKey, list);
    },
    onError: (err) => {
      setAnnouncement("");
      // A newer order, or another set of Routes, than the one the move was made on.
      if (
        isStale(err) ||
        (isApiError(err) && err.errors?.some((e) => e.code === "route_set_mismatch") === true)
      ) {
        setStale(true);
      }
    },
    onSettled: () => setPending(null),
  });

  const routes = query.data?.data.items ?? [];
  const movable = routes.filter((r) => !r.is_default);
  const defaults = routes.filter((r) => r.is_default);
  const serverIds = movable.map((r) => r.id);
  const ids = pending ?? serverIds;
  const order = ids.join(",");

  // A moved row's button keeps the focus: the browser drops it when the row's element moves, so it is put back once
  // the list shows the new order.
  useLayoutEffect(() => {
    if (focusMove.current !== null && focusMove.current.order === order) {
      document.querySelector<HTMLElement>(`[data-move="${focusMove.current.button}"]`)?.focus();
      focusMove.current = null;
    }
  });
  const byId = new Map(movable.map((r) => [r.id, r]));
  const ordered = ids.flatMap((id) => {
    const r = byId.get(id);
    return r === undefined ? [] : [r];
  });

  const defaultsCount = defaults.length;
  const showsFocusTarget = focusRouteId !== undefined && routes.some((r) => r.id === focusRouteId);
  useEffect(() => {
    if (!showsFocusTarget) {
      return;
    }
    document.querySelector<HTMLElement>(`[data-route-link="${focusRouteId}"]`)?.focus();
    onFocused?.();
  }, [focusRouteId, showsFocusTarget, onFocused]);

  const commit = (next: string[], moved: string) => {
    const etag = query.data?.etag;
    if (etag === undefined || etag === "" || sameOrder(next, ids) || save.isPending) {
      focusMove.current = null;
      return;
    }
    const route = byId.get(moved);
    setAnnouncement(
      route === undefined
        ? ""
        : t("routes.list.moved", {
            name: route.name,
            position: next.indexOf(moved) + 1,
            total: next.length + defaultsCount,
          }),
    );
    setStale(false);
    save.reset();
    setPending(next);
    save.mutate({ ids: next, etag });
  };

  const onDragStart = (id: string) => (e: DragEvent<HTMLElement>, row: HTMLLIElement | null) => {
    if (save.isPending) {
      e.preventDefault();
      return;
    }
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", id);
    if (row !== null) {
      e.dataTransfer.setDragImage(row, 16, 16);
    }
    setDragging(id);
  };
  const endDrag = () => {
    setDragging(null);
    setDropTarget(null);
  };
  const dragProps = (route: Route) => ({
    onDragOver: (e: DragEvent<HTMLLIElement>) => {
      if (dragging === null) {
        return;
      }
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      const rect = e.currentTarget.getBoundingClientRect();
      const before = route.is_default || e.clientY < rect.top + rect.height / 2;
      if (dropTarget?.id !== route.id || dropTarget.before !== before) {
        setDropTarget({ id: route.id, before });
      }
    },
    onDrop: (e: DragEvent<HTMLLIElement>) => {
      if (dragging === null) {
        return;
      }
      e.preventDefault();
      const moved = dragging;
      const target = dropTarget;
      endDrag();
      if (target !== null && target.id !== moved) {
        const isDefault = byId.get(target.id) === undefined;
        commit(moveTo(ids, moved, isDefault ? null : target.id, target.before), moved);
      }
    },
  });

  if (query.data === undefined) {
    return (
      <p className="text-sm text-muted-foreground" role="status">
        {query.isError ? problemText(t, query.error) : t("common.loading")}
      </p>
    );
  }
  const canMove = canWrite;
  return (
    <div className="flex flex-col gap-3">
      {stale && (
        <Alert variant="destructive" data-testid="route-order-stale">
          <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
            <span>{t("routes.list.stale")}</span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                setStale(false);
                save.reset();
                void queryClient.invalidateQueries({ queryKey: getListRoutesQueryKey() });
              }}
            >
              {t("common.reload")}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {save.isError && !stale && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">{problemText(t, save.error)}</AlertDescription>
        </Alert>
      )}
      <p className="sr-only" role="status" aria-live="polite">
        {announcement}
      </p>
      <ol
        className="rounded-lg border"
        aria-label={t("routes.list.label")}
        aria-busy={save.isPending}
        data-testid="route-list"
      >
        {ordered.map((route, index) => (
          <RouteRow
            key={route.id}
            route={route}
            number={index + 1}
            canMove={canMove}
            first={index === 0}
            last={index === ordered.length - 1}
            busy={save.isPending}
            dropTarget={dragging !== null && dragging !== route.id ? dropTarget : null}
            onDragStart={onDragStart(route.id)}
            onDragEnd={endDrag}
            onMove={(step) => {
              const next = moveBy(ids, route.id, step);
              focusMove.current = {
                button: `${route.id}-${step < 0 ? "up" : "down"}`,
                order: next.join(","),
              };
              commit(next, route.id);
            }}
            dragProps={dragProps(route)}
          />
        ))}
        {defaults.map((route) => (
          <RouteRow
            key={route.id}
            route={route}
            number={ordered.length + 1}
            canMove={false}
            first={false}
            last
            busy={false}
            dropTarget={dragging !== null ? dropTarget : null}
            onDragStart={() => {}}
            onDragEnd={endDrag}
            onMove={() => {}}
            dragProps={dragProps(route)}
          />
        ))}
      </ol>
    </div>
  );
}
