// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Destinations (C-13.FR-9, C-11.FR-9, C-22.FR-1): every Destination of every type with its type — a Bridge by the
// messenger its Connection's bridge names — name, "Connection · channel" (an outgoing webhook: the host of its URL;
// D309), health and Routes, page by page, and the Broken banner of each Broken Destination above them; "Create
// destination" with destinations:write. The destination hint reads the list again as health changes.

import { Link, createFileRoute } from "@tanstack/react-router";
import { createContext, useContext, useMemo } from "react";
import { useTranslation } from "react-i18next";

import {
  getListDestinationsQueryKey,
  listDestinations,
} from "../api/gen/endpoints/destinations/destinations";
import type { Connection, Destination } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { BrokenBanner, HealthBadge } from "../components/destination-health";
import { PageTitle } from "../components/docs-link";
import {
  connectionPlace,
  destinationMessengerName,
  useConnectionsById,
} from "../components/destination-label";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/destinations/")({
  staticData: { shell: true },
  component: DestinationsPage,
});

function DestinationLink({ destination }: { destination: Destination }) {
  return (
    <Link
      to="/destinations/$destinationId"
      params={{ destinationId: destination.id }}
      className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
    >
      {destination.name}
    </Link>
  );
}

function NameCell({ row }: { row: Destination }) {
  return <DestinationLink destination={row} />;
}

/** The Connections by id, read once by the table for every row's place. */
const ConnectionsById = createContext<ReadonlyMap<string, Connection>>(new Map());

function PlaceCell({ row }: { row: Destination }) {
  const place = connectionPlace(row, useContext(ConnectionsById));
  return place === undefined ? null : (
    <span className="wrap-anywhere" data-testid="destination-place">
      {place}
    </span>
  );
}

function HealthCell({ row }: { row: Destination }) {
  return <HealthBadge health={row.health} />;
}

function RoutesCell({ row }: { row: Destination }) {
  const canRoutes = useCan("routes:read");
  if (row.routes.length === 0) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <ul className="flex flex-wrap gap-x-2 gap-y-1" data-testid="destination-routes">
      {row.routes.map((r) => (
        <li key={r.id} className="wrap-anywhere">
          {canRoutes ? (
            <Link
              to="/routes/$routeId"
              params={{ routeId: r.id }}
              className="text-primary underline-offset-4 hover:underline focus-visible:underline"
            >
              {r.name}
            </Link>
          ) : (
            r.name
          )}
        </li>
      ))}
    </ul>
  );
}

function DestinationsTable() {
  const { t } = useTranslation();
  const list = useCursorList<Destination>(getListDestinationsQueryKey(), (cursor, signal) =>
    listDestinations({ cursor }, { signal }),
  );
  const connections = useConnectionsById();
  const columns = useMemo(
    (): DataColumn<Destination>[] => [
      {
        id: "type",
        header: t("destinations.fields.type"),
        text: (row) =>
          destinationMessengerName(
            t,
            row.type,
            row.type === "webhook" ? undefined : connections.get(row.connection_id),
          ),
      },
      { id: "name", header: t("destinations.fields.name"), className: "min-w-32", Cell: NameCell },
      {
        id: "place",
        header: t("destinations.fields.place"),
        className: "min-w-40",
        Cell: PlaceCell,
      },
      { id: "health", header: t("destinations.fields.health"), Cell: HealthCell },
      {
        id: "routes",
        header: t("destinations.fields.routes"),
        className: "min-w-32",
        Cell: RoutesCell,
      },
    ],
    [t, connections],
  );
  const broken = list.items.filter((d) => d.health.state === "broken");
  return (
    <div className="flex min-w-0 flex-col gap-4">
      {broken.length > 0 && (
        <section
          className="flex flex-col gap-2"
          aria-label={t("destinations.list.broken")}
          data-testid="broken-destinations"
        >
          {broken.map((d) => (
            <BrokenBanner key={d.id} health={d.health} name={<DestinationLink destination={d} />} />
          ))}
        </section>
      )}
      <ConnectionsById value={connections}>
        <DataTable
          label={t("destinations.title")}
          columns={columns}
          list={list}
          rowId={(row) => row.id}
          empty={t("destinations.list.empty")}
        />
      </ConnectionsById>
    </div>
  );
}

function DestinationsPage() {
  const { t } = useTranslation();
  const canWrite = useCan("destinations:write");
  return (
    <RequirePermission permission="destinations:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-col gap-1">
            <PageTitle docs="page.destinations" about={t("destinations.title")}>
              {t("destinations.title")}
            </PageTitle>
            <p className="text-muted-foreground">{t("destinations.hint")}</p>
          </div>
          {canWrite && (
            <Link to="/destinations/new" className={buttonVariants()}>
              {t("destinations.create.start")}
            </Link>
          )}
        </div>
        <DestinationsTable />
      </div>
    </RequirePermission>
  );
}
