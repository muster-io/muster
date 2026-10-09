// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Destination (C-13.FR-9, FR-10; C-11.FR-9; C-14.FR-2, FR-14; C-15.FR-1, FR-5, FR-10): its health with the Broken
// banner, its Routes, and its form — Mattermost, Telegram, which shows the channel and the discussion group it found,
// or the outgoing webhook, followed by its Secrets and its Signing secret — read-only without destinations:write;
// "Check" with destinations:test for the types that have a Destination check, and "Delete" with destinations:write.
// The form keeps the version it was read at and sends it as If-Match: when the Destination changes elsewhere an
// untouched form takes the new version and says so, and a form with changes keeps them, and its save is refused (412)
// with the offer to reload. A new version with the same settings — a Secret or the Signing secret changed — becomes the
// form's version without a word, whether the form has changes or not. Health comes and goes without a reload: from the
// destination hint, and at once from a check's result.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { getListConnectionsQueryKey } from "../api/gen/endpoints/connections/connections";
import {
  getGetDestinationQueryKey,
  getGetDestinationQueryOptions,
  getListDestinationsQueryKey,
  updateDestination,
  useGetDestination,
} from "../api/gen/endpoints/destinations/destinations";
import type {
  Destination,
  MattermostDestination,
  TelegramDestination,
  WebhookDestination,
} from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { DestinationCheck } from "../components/destination-check";
import { DestinationDeleteDialog } from "../components/destination-delete-dialog";
import {
  type DestinationKind,
  DestinationForm,
  versionAction,
} from "../components/destination-form";
import { DestinationSecrets, refreshWebhook } from "../components/destination-secrets";
import { BrokenBanner, HealthBadge, destinationTypeName } from "../components/destination-health";
import { MATTERMOST_KIND } from "../components/mattermost-destination-fields";
import { SigningSecret } from "../components/signing-secret";
import { TELEGRAM_KIND } from "../components/telegram-destination-fields";
import { Alert, AlertDescription } from "../components/ui/alert";
import { buttonVariants } from "../components/ui/button";
import { WEBHOOK_KIND } from "../components/webhook-destination-fields";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/destinations/$destinationId")({
  staticData: { shell: true },
  component: DestinationPage,
});

/** The types whose form is on this page. */
type Editable = MattermostDestination | TelegramDestination | WebhookDestination;

function isEditable(d: Destination): d is Editable {
  return d.type === "mattermost" || d.type === "telegram" || d.type === "webhook";
}

/** The types with a Destination check; the outgoing webhook has none. */
function hasCheck(d: Destination): boolean {
  return d.type === "mattermost" || d.type === "telegram";
}

/** The version of the form: health is not part of it, so a change of health alone never replaces the form. */
function versionOf(d: Destination): string {
  return d.etag;
}

function EditForm<V>({ current, kind }: { current: Editable; kind: DestinationKind<V> }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const canWrite = useCan("destinations:write");
  const canTest = useCan("destinations:test");
  const [base, setBase] = useState(current);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [replaced, setReplaced] = useState(false);
  const [reloadError, setReloadError] = useState<unknown>(null);
  // The form is mounted again only for a version it did not save itself: a newer one or a reload.
  const [formKey, setFormKey] = useState(0);
  // A newer version with the same settings — its Secrets or its Signing secret changed — is the form's version from now
  // on, changes or not; another newer version (another Admin, another tab) replaces an untouched form, which says so.
  const action = saving ? "same" : versionAction(kind, current, base, dirty);
  if (action === "adopt") {
    setBase(current);
  } else if (action === "replace") {
    setBase(current);
    setReplaced(true);
    setSaved(false);
    setFormKey((k) => k + 1);
  }
  return (
    <>
      {replaced && (
        <Alert data-testid="destination-replaced">
          <AlertDescription>{t("destinations.errors.changedElsewhere")}</AlertDescription>
        </Alert>
      )}
      {reloadError !== null && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, reloadError)}
          </AlertDescription>
        </Alert>
      )}
      {canTest && hasCheck(current) && <DestinationCheck destination={current} dirty={dirty} />}
      <DestinationForm
        key={formKey}
        kind={kind}
        destination={base}
        readOnly={!canWrite}
        submitLabel={t("common.save")}
        stale={versionOf(current) !== versionOf(base)}
        saved={saved}
        onDirtyChange={setDirty}
        save={async (input) => {
          setSaving(true);
          setSaved(false);
          try {
            await queryClient.cancelQueries({ queryKey: getGetDestinationQueryKey(base.id) });
            const updated = await updateDestination(base.id, input, {
              headers: { "If-Match": base.etag },
            });
            // A read that started before the save committed must not bring back the version it replaced.
            await queryClient.cancelQueries({ queryKey: getGetDestinationQueryKey(base.id) });
            queryClient.setQueryData(getGetDestinationQueryKey(updated.id), updated);
            void queryClient.invalidateQueries({ queryKey: getListDestinationsQueryKey() });
            void queryClient.invalidateQueries({ queryKey: getListConnectionsQueryKey() });
            if (updated.type === "webhook") {
              // The new version is the ETag of the Secrets too.
              refreshWebhook(queryClient, updated.id);
            }
            if (!isEditable(updated)) {
              return undefined;
            }
            setDirty(false);
            setReplaced(false);
            setBase(updated);
            setSaved(true);
            return updated;
          } finally {
            setSaving(false);
          }
        }}
        onReload={() => {
          setReloadError(null);
          queryClient
            .fetchQuery({ ...getGetDestinationQueryOptions(base.id), staleTime: 0 })
            .then((fresh) => {
              if (isEditable(fresh)) {
                setDirty(false);
                setReplaced(false);
                setSaved(false);
                setBase(fresh);
                setFormKey((k) => k + 1);
              }
            })
            .catch((err: unknown) => setReloadError(err));
        }}
      />
    </>
  );
}

function RoutesOf({ destination }: { destination: Destination }) {
  const { t } = useTranslation();
  const canRoutes = useCan("routes:read");
  return (
    <section className="flex flex-col gap-1" data-testid="destination-routes">
      <h2 className="text-sm font-medium">{t("destinations.fields.routes")}</h2>
      {destination.routes.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("destinations.page.noRoutes")}</p>
      ) : (
        <ul className="flex flex-wrap gap-x-3 gap-y-1 text-sm">
          {destination.routes.map((r) => (
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
      )}
    </section>
  );
}

function DestinationView({ destinationId }: { destinationId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("destinations:write");
  const query = useGetDestination(destinationId);
  const destination = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/destinations"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("destinations.title")}
        </Link>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <h1 className="min-w-0 text-2xl font-semibold tracking-tight wrap-anywhere">
              {destination?.name ?? t("destinations.edit.title")}
            </h1>
            {destination !== undefined && (
              <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
                <span data-testid="destination-type">
                  {destinationTypeName(t, destination.type)}
                </span>
                <HealthBadge health={destination.health} />
              </p>
            )}
          </div>
          {/* Outside the form, which a newer version replaces; Delete sends the version read last. */}
          {canWrite && destination !== undefined && (
            <DestinationDeleteDialog destination={destination} />
          )}
        </div>
      </div>
      {destination === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <>
          <div role="status" aria-live="polite" className="empty:hidden">
            <BrokenBanner health={destination.health} />
          </div>
          <RoutesOf destination={destination} />
          {destination.type === "mattermost" ? (
            <EditForm current={destination} kind={MATTERMOST_KIND} />
          ) : destination.type === "telegram" ? (
            <EditForm current={destination} kind={TELEGRAM_KIND} />
          ) : (
            <>
              <EditForm current={destination} kind={WEBHOOK_KIND} />
              <DestinationSecrets destinationId={destination.id} />
              <SigningSecret destination={destination} />
            </>
          )}
        </>
      )}
    </div>
  );
}

function DestinationPage() {
  const { destinationId } = Route.useParams();
  return (
    <RequirePermission permission="destinations:read">
      <DestinationView destinationId={destinationId} />
    </RequirePermission>
  );
}
