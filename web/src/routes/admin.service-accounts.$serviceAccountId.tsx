// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Service account's page (C-04.FR-2): the details; for service-accounts:write the Role change (If-Match), disable,
// enable and delete; and its tokens: "Create token" with a name and an optional expiry (the value shown once), the list
// with the last use, and "Revoke". Its tokens act with its Role.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createServiceAccountToken,
  deleteServiceAccount,
  getGetServiceAccountQueryKey,
  getGetServiceAccountQueryOptions,
  getListServiceAccountTokensQueryKey,
  getListServiceAccountsQueryKey,
  revokeServiceAccountToken,
  updateServiceAccount,
  useDisableServiceAccount,
  useEnableServiceAccount,
  useGetServiceAccount,
  useListServiceAccountTokens,
} from "../api/gen/endpoints/api-tokens/api-tokens";
import type { RoleName, ServiceAccount } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { TokenList } from "../components/profile-tokens";
import { serviceAccountFieldText } from "../components/service-account-dialog";
import { TokenCreateDialog } from "../components/token-created-dialog";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button, buttonVariants } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { Label } from "../components/ui/label";
import { NativeSelect, NativeSelectOption } from "../components/ui/native-select";
import { roleLabel, statusLabel, useRoleNames } from "../components/user-create-dialog";
import { isApiError, isStale, problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";

export const Route = createFileRoute("/admin/service-accounts/$serviceAccountId")({
  staticData: { shell: true },
  component: ServiceAccountPage,
});

/** Keeps the cached account and the list in step with a changed account. */
function useStoreAccount() {
  const queryClient = useQueryClient();
  return (account: ServiceAccount) => {
    queryClient.setQueryData(getGetServiceAccountQueryKey(account.id), account);
    void queryClient.invalidateQueries({ queryKey: getListServiceAccountsQueryKey() });
  };
}

function Details({ account }: { account: ServiceAccount }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const rows: [string, ReactNode, string][] = [
    [t("serviceAccounts.fields.role"), roleLabel(t, account.role), "role"],
    [t("serviceAccounts.fields.status"), statusLabel(t, account.status), "status"],
    [t("serviceAccounts.fields.tokens"), String(account.token_count), "tokens"],
    [t("serviceAccounts.fields.created"), dateTime(account.created_at), "created"],
  ];
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("serviceAccounts.page.details")}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
          {rows.map(([label, value, id]) => (
            <div key={id} className="contents">
              <dt className="text-muted-foreground">{label}</dt>
              <dd className="min-w-0 break-words" data-testid={`sa-${id}`}>
                {value}
              </dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}

/**
 * The Role. The card keeps the version it was read at and sends it as If-Match; a newer version (an action on this
 * page, another Admin) replaces an untouched choice, while a changed one is kept and a save over the newer version is
 * refused.
 */
function RoleCard({ account }: { account: ServiceAccount }) {
  const { t } = useTranslation();
  const store = useStoreAccount();
  const queryClient = useQueryClient();
  const roles = useRoleNames();
  const [base, setBase] = useState(account);
  const [role, setRole] = useState<RoleName>(account.role);
  const [saved, setSaved] = useState(false);
  const update = useMutation({
    mutationFn: (next: RoleName) =>
      updateServiceAccount(
        base.id,
        { name: base.name, role: next },
        { headers: { "If-Match": base.etag } },
      ),
    onMutate: () => queryClient.cancelQueries({ queryKey: getGetServiceAccountQueryKey(base.id) }),
    onSuccess: (updated) => {
      setBase(updated);
      setRole(updated.role);
      store(updated);
      setSaved(true);
    },
  });
  const dirty = role !== base.role;
  if (account.etag !== base.etag && !dirty && !update.isPending) {
    setBase(account);
    setRole(account.role);
  }
  const stale = (account.etag !== base.etag && !update.isPending) || isStale(update.error);
  const reload = () => {
    update.reset();
    void queryClient
      .fetchQuery({ ...getGetServiceAccountQueryOptions(base.id), staleTime: 0 })
      .then((fresh) => {
        setBase(fresh);
        setRole(fresh.role);
      });
  };
  const fieldError =
    isApiError(update.error) && update.error.errors?.length
      ? update.error.errors.map((e) => serviceAccountFieldText(t, e.code)).join(" ")
      : undefined;
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("serviceAccounts.fields.role")}</h2>
        </CardTitle>
        <CardDescription>{t("serviceAccounts.page.roleHint")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          noValidate
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            setSaved(false);
            update.mutate(role);
          }}
        >
          <div className="flex flex-col gap-2">
            <Label htmlFor="sa-role">{t("serviceAccounts.fields.role")}</Label>
            <NativeSelect
              id="sa-role"
              className="w-full max-w-sm"
              value={role}
              aria-invalid={fieldError !== undefined}
              aria-describedby={fieldError ? "sa-role-error" : undefined}
              onChange={(e) => {
                setSaved(false);
                setRole(roles.find((r) => r === e.target.value) ?? base.role);
              }}
            >
              {roles.map((r) => (
                <NativeSelectOption key={r} value={r}>
                  {roleLabel(t, r)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            {fieldError && (
              <p id="sa-role-error" className="text-sm text-destructive">
                {fieldError}
              </p>
            )}
          </div>
          {stale && (
            <Alert variant="destructive">
              <AlertDescription className="flex flex-wrap items-center gap-3 text-current">
                <span>{t("serviceAccounts.errors.stale")}</span>
                <Button variant="outline" size="sm" onClick={reload}>
                  {t("common.reload")}
                </Button>
              </AlertDescription>
            </Alert>
          )}
          {update.isError && fieldError === undefined && !isStale(update.error) && (
            <Alert variant="destructive">
              <AlertDescription className="text-current">
                {problemText(t, update.error)}
              </AlertDescription>
            </Alert>
          )}
          <div className="flex items-center gap-3">
            <Button type="submit" disabled={update.isPending || !dirty}>
              {t("common.save")}
            </Button>
            <span role="status" className="text-sm text-muted-foreground">
              {saved && !dirty ? t("common.saved") : ""}
            </span>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function ActionsCard({ account }: { account: ServiceAccount }) {
  const { t } = useTranslation();
  const store = useStoreAccount();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const onAccount = { mutation: { onSuccess: store } };
  const disable = useDisableServiceAccount(onAccount);
  const enable = useEnableServiceAccount(onAccount);
  const remove = useMutation({
    mutationFn: () => deleteServiceAccount(account.id, { headers: { "If-Match": account.etag } }),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: getGetServiceAccountQueryKey(account.id) });
      void queryClient.invalidateQueries({ queryKey: getListServiceAccountsQueryKey() });
      void navigate({ to: "/admin/service-accounts" });
    },
    // A newer version was read meanwhile: the page reads it again, and a second Delete sends it.
    onError: (err) => {
      if (isStale(err)) {
        void queryClient.invalidateQueries({
          queryKey: getGetServiceAccountQueryKey(account.id),
        });
      }
    },
  });
  const failed = [disable, enable].find((m) => m.isError);
  const busy = [disable, enable, remove].some((m) => m.isPending);
  const run = (mutate: () => void) => {
    disable.reset();
    enable.reset();
    mutate();
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("serviceAccounts.page.actions")}</h2>
        </CardTitle>
        <CardDescription>{t("serviceAccounts.page.actionsHint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap gap-2">
          {account.status === "active" ? (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => run(() => disable.mutate({ serviceAccountId: account.id }))}
            >
              {t("serviceAccounts.actions.disable")}
            </Button>
          ) : (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => run(() => enable.mutate({ serviceAccountId: account.id }))}
            >
              {t("serviceAccounts.actions.enable")}
            </Button>
          )}
          <Button
            variant="destructive"
            disabled={busy}
            onClick={() => {
              remove.reset();
              setDeleting(true);
            }}
          >
            {t("serviceAccounts.actions.delete")}
          </Button>
        </div>
        {failed?.error !== undefined && failed.error !== null && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {problemText(t, failed.error)}
            </AlertDescription>
          </Alert>
        )}
        <Dialog open={deleting} onOpenChange={setDeleting}>
          <DialogContent closeLabel={t("common.close")}>
            <DialogHeader>
              <DialogTitle className="break-words">
                {t("serviceAccounts.actions.deleteTitle", { name: account.name })}
              </DialogTitle>
              <DialogDescription>{t("serviceAccounts.actions.deleteConfirm")}</DialogDescription>
            </DialogHeader>
            {remove.isError && (
              <Alert variant="destructive">
                <AlertDescription className="text-current">
                  {isStale(remove.error)
                    ? t("serviceAccounts.errors.stale")
                    : problemText(t, remove.error)}
                </AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
              <Button
                variant="destructive"
                disabled={remove.isPending}
                onClick={() => remove.mutate()}
              >
                {t("serviceAccounts.actions.delete")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}

function TokensCard({ account, canWrite }: { account: ServiceAccount; canWrite: boolean }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const tokens = useListServiceAccountTokens(account.id);
  // The token count of the account changes with its tokens.
  const refresh = () => {
    void queryClient.invalidateQueries({
      queryKey: getListServiceAccountTokensQueryKey(account.id),
    });
    void queryClient.invalidateQueries({ queryKey: getGetServiceAccountQueryKey(account.id) });
    void queryClient.invalidateQueries({ queryKey: getListServiceAccountsQueryKey() });
  };
  return (
    <Card className="lg:col-span-2">
      <CardHeader>
        <CardTitle>
          <h2>{t("serviceAccounts.page.tokens")}</h2>
        </CardTitle>
        <CardDescription>{t("serviceAccounts.page.tokensHint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {canWrite && (
          <div>
            <TokenCreateDialog
              description={t("serviceAccounts.page.createTokenHint", { name: account.name })}
              create={(input) =>
                createServiceAccountToken(account.id, {
                  name: input.name,
                  expires_at: input.expires_at,
                })
              }
              onCreated={refresh}
            />
          </div>
        )}
        <TokenList
          label={t("serviceAccounts.page.tokens")}
          tokens={tokens.data?.items ?? []}
          loading={tokens.isPending}
          error={tokens.error}
          empty={t("serviceAccounts.page.noTokens")}
          revoke={canWrite ? (token) => revokeServiceAccountToken(account.id, token.id) : undefined}
          onRevoked={refresh}
        />
      </CardContent>
    </Card>
  );
}

function ServiceAccountView({ serviceAccountId }: { serviceAccountId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("service-accounts:write");
  const query = useGetServiceAccount(serviceAccountId);
  const account = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/admin/service-accounts"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("serviceAccounts.title")}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight break-words">
          {account?.name ?? t("serviceAccounts.page.title")}
        </h1>
      </div>
      {account === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <div className="grid gap-6 lg:grid-cols-2">
          <Details account={account} />
          {canWrite && <RoleCard key={account.id} account={account} />}
          {canWrite && <ActionsCard account={account} />}
          <TokensCard account={account} canWrite={canWrite} />
        </div>
      )}
    </div>
  );
}

function ServiceAccountPage() {
  const { serviceAccountId } = Route.useParams();
  return (
    <RequirePermission permission="service-accounts:read">
      <ServiceAccountView serviceAccountId={serviceAccountId} />
    </RequirePermission>
  );
}
