// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Connection (C-13.FR-1, FR-2, FR-13; C-14.FR-1, FR-11): its form, read-only without connections:write; for
// Mattermost the callback address with its hint; "Check connection" and "Delete" with connections:write. A
// Telegram Connection's check takes the base URL in the form: an unsaved one gets only the dry probe, without the
// token. The form keeps the version it was
// read at and sends it as If-Match: when the Connection changes elsewhere an untouched form takes the new version and
// says so, and a form with changes keeps them, and its save is refused (412) with the offer to reload.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getGetConnectionQueryKey,
  getGetConnectionQueryOptions,
  getListConnectionsQueryKey,
  updateConnection,
  useGetConnection,
} from "../api/gen/endpoints/connections/connections";
import type { Connection } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { CallbackHint } from "../components/callback-hint";
import { ConnectionCheck } from "../components/connection-check";
import { ConnectionDeleteDialog, ConnectionForm } from "../components/connection-form";
import { normalizeBaseUrl } from "../components/telegram-connection-fields";
import { Alert, AlertDescription } from "../components/ui/alert";
import { buttonVariants } from "../components/ui/button";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/connections/$connectionId")({
  staticData: { shell: true },
  component: ConnectionPage,
});

/** The bot's name as its messenger writes it: Telegram with an @. */
function botName(c: Connection): string | undefined {
  if (c.bot_username === undefined || c.bot_username === null || c.bot_username === "") {
    return undefined;
  }
  return c.type === "telegram" ? `@${c.bot_username}` : c.bot_username;
}

function EditForm({ current }: { current: Connection }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const canWrite = useCan("connections:write");
  const [base, setBase] = useState(current);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [replaced, setReplaced] = useState(false);
  const [reloadError, setReloadError] = useState<unknown>(null);
  // The base URL in the form of a Telegram Connection, as it is typed.
  const [baseUrl, setBaseUrl] = useState<string>();
  const unsavedBaseUrl =
    base.type === "telegram" &&
    baseUrl !== undefined &&
    normalizeBaseUrl(baseUrl) !== base.bot_api_base_url
      ? baseUrl
      : undefined;
  // The form is mounted again only for a version it did not save itself: a newer one or a reload.
  const [formKey, setFormKey] = useState(0);
  // A newer version (another Admin, another tab) replaces an untouched form, which says so.
  if (current.etag !== base.etag && !dirty && !saving) {
    setBase(current);
    setReplaced(true);
    setSaved(false);
    setFormKey((k) => k + 1);
  }
  return (
    <>
      {replaced && (
        <Alert data-testid="connection-replaced">
          <AlertDescription>{t("connections.errors.changedElsewhere")}</AlertDescription>
        </Alert>
      )}
      {reloadError !== null && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, reloadError)}
          </AlertDescription>
        </Alert>
      )}
      <ConnectionForm
        key={formKey}
        connection={base}
        readOnly={!canWrite}
        submitLabel={t("common.save")}
        stale={current.etag !== base.etag}
        saved={saved}
        onDirtyChange={setDirty}
        onBaseUrlChange={setBaseUrl}
        save={async (input) => {
          setSaving(true);
          setSaved(false);
          try {
            await queryClient.cancelQueries({ queryKey: getGetConnectionQueryKey(base.id) });
            const updated = await updateConnection(base.id, input, {
              headers: { "If-Match": base.etag },
            });
            // A read that started before the save committed must not bring back the version it replaced.
            await queryClient.cancelQueries({ queryKey: getGetConnectionQueryKey(base.id) });
            queryClient.setQueryData(getGetConnectionQueryKey(updated.id), updated);
            void queryClient.invalidateQueries({ queryKey: getListConnectionsQueryKey() });
            if (updated.type !== base.type) {
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
            .fetchQuery({ ...getGetConnectionQueryOptions(base.id), staleTime: 0 })
            .then((fresh) => {
              if (fresh.type === base.type) {
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
      {current.type === "mattermost" && <CallbackHint callbackUrl={current.callback_url} />}
      {/* A saved change of the address or the bot token makes an earlier result stale, and so does another unsaved
          address: a result belongs to the address it was made for. */}
      {canWrite && (
        <ConnectionCheck
          key={`${base.etag} ${unsavedBaseUrl ?? ""}`}
          connectionId={base.id}
          type={base.type}
          dirty={dirty}
          unsavedBaseUrl={unsavedBaseUrl}
          updateMode={base.type === "telegram" ? base.update_mode : undefined}
        />
      )}
    </>
  );
}

function ConnectionView({ connectionId }: { connectionId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("connections:write");
  const query = useGetConnection(connectionId);
  const connection = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/connections"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("connections.title")}
        </Link>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <h1 className="min-w-0 text-2xl font-semibold tracking-tight wrap-anywhere">
              {connection?.name ?? t("connections.edit.title")}
            </h1>
            {connection !== undefined && (
              <p
                className="text-sm wrap-anywhere text-muted-foreground"
                data-testid="connection-bot"
              >
                {botName(connection) === undefined
                  ? t("connections.edit.botUnknown")
                  : t("connections.edit.bot", { bot: botName(connection) })}
              </p>
            )}
          </div>
          {/* Outside the form, which a newer version replaces; Delete sends the version read last. */}
          {canWrite && connection !== undefined && (
            <ConnectionDeleteDialog connection={connection} />
          )}
        </div>
      </div>
      {connection === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <EditForm current={connection} />
      )}
    </div>
  );
}

function ConnectionPage() {
  const { connectionId } = Route.useParams();
  return (
    <RequirePermission permission="connections:read">
      <ConnectionView connectionId={connectionId} />
    </RequirePermission>
  );
}
