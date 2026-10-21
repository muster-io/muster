// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Connection (C-13.FR-1, FR-2, FR-13; C-14.FR-1, FR-11; C-22.FR-1, FR-2, FR-18; C-24.FR-1, FR-2): its form, read-only
// without connections:write; for Mattermost the callback address with its hint, for a Bridge in push mode and Slack in
// HTTP mode the same in its form;
// "Check connection" and "Delete" with connections:write. The check reads the saved Connection while the form holds no
// changes, and checks the form's values as a draft while it does (C-13.FR-14), which saves nothing: the server sends
// the stored token only to the saved address through the saved proxy. A Bridge Connection shows what its bridge offers,
// from the info the last check of the saved Connection read, and its form proposes half of the bridge's rate as the
// limiter.
// The form keeps the version it was read at and sends it as If-Match: when the Connection changes elsewhere an
// untouched form takes the new version and says so, and a form with changes keeps them, and its save is refused (412)
// with the offer to reload. The tab "Request log" (C-23.FR-6) lists the Connection's requests, with the tab and its
// filters in the URL; the form stays mounted behind it, so that its changes last. A Slack Connection shows what its last
// check learned — the bot, the workspace and the app — and, while the Leader's last Socket Mode hello counted another
// connection of its app, the warning that another program shares its presses.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
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
import { BridgeInfoPanel } from "../components/bridge-connection-fields";
import { CallbackHint } from "../components/callback-hint";
import { ConnectionCheck } from "../components/connection-check";
import { PageTitle } from "../components/docs-link";
import {
  ConnectionDeleteDialog,
  ConnectionForm,
  type ConnectionValues,
  connectionDraft,
  draftErrorText,
} from "../components/connection-form";
import { RequestLogTabs } from "../components/request-log";
import { SlackConnectionInfo, SlackSharedAppWarning } from "../components/slack-connection-fields";
import { type RequestLogSearch, requestLogSearchSchema } from "../components/request-log-search";
import { Alert, AlertDescription } from "../components/ui/alert";
import { buttonVariants } from "../components/ui/button";
import { problemText } from "../lib/api";
import { connectionDocs } from "../lib/docs";

export const Route = createFileRoute("/connections/$connectionId")({
  validateSearch: requestLogSearchSchema,
  staticData: { shell: true },
  component: ConnectionPage,
});

/** The types whose Connections the page edits with the form; a type this SPA does not know has none. */
const FORM_TYPES: ReadonlySet<Connection["type"]> = new Set([
  "mattermost",
  "telegram",
  "bridge",
  "slack",
]);

/** The bot's name as its messenger writes it: Telegram with an @; none for a Bridge. */
function botName(c: Connection): string | undefined {
  if (c.type === "bridge") {
    return undefined;
  }
  if (c.type === "slack") {
    return c.bot_name || undefined;
  }
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
  // The values in the form as they are typed; while the form holds changes, the check sends them as a draft.
  const [values, setValues] = useState<ConnectionValues>();
  const edited = dirty && values !== undefined && values.type === base.type ? values : undefined;
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
        onValuesChange={setValues}
        info={current.type === "bridge" ? current.info : undefined}
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
      {/* A saved change makes an earlier result stale, and so does a change of the form: a result belongs to the values
          it was made for. */}
      {canWrite && (
        <ConnectionCheck
          key={`check ${base.etag}`}
          connectionId={base.id}
          type={base.type}
          draft={edited === undefined ? undefined : connectionDraft(edited, base)}
          invalid={edited === undefined ? undefined : draftErrorText(t, edited, base)}
          updateMode={
            base.type !== "telegram"
              ? undefined
              : edited === undefined
                ? base.update_mode
                : edited.update_mode
          }
          appTokenStored={base.type === "slack" && base.app_token_status.set}
        />
      )}
      {/* The stored info, which a check refreshes without a new version of the Connection. */}
      {current.type === "bridge" && (
        <BridgeInfoPanel info={current.info} readAt={current.info_read_at} />
      )}
    </>
  );
}

function ConnectionView({ connectionId }: { connectionId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("connections:write");
  // A Connection being deleted is read no more: the read would answer 404.
  const [deleting, setDeleting] = useState(false);
  const query = useGetConnection(connectionId, { query: { enabled: !deleting } });
  const connection = query.data;
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const onSearch = (patch: Partial<RequestLogSearch>) =>
    void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
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
            <PageTitle
              docs={connection === undefined ? "page.connections" : connectionDocs(connection.type)}
            >
              {connection?.name ?? t("connections.edit.title")}
            </PageTitle>
            {connection?.type === "slack" && <SlackConnectionInfo connection={connection} />}
            {connection !== undefined &&
              connection.type !== "bridge" &&
              connection.type !== "slack" && (
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
            <ConnectionDeleteDialog connection={connection} onDeletingChange={setDeleting} />
          )}
        </div>
      </div>
      {connection === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : FORM_TYPES.has(connection.type) ? (
        <RequestLogTabs search={search} onSearch={onSearch} scope={{ connection: connection.id }}>
          {connection.type === "slack" && <SlackSharedAppWarning connection={connection} />}
          <EditForm current={connection} />
        </RequestLogTabs>
      ) : null}
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
