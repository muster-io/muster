// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Personal access tokens of the profile (C-04.FR-1, FR-3, FR-7, FR-8, C-03.FR-12): the list without values, with
// each token's Permissions, expiry and last use, "Revoke", and "Create token", which offers only the Permissions the
// user holds. For an OIDC account without an offline token, the date until which the tokens work without a new OIDC
// sign-in. The token list and its revoke dialog also serve the Service account page.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createPersonalAccessToken,
  getListPersonalAccessTokensQueryKey,
  revokePersonalAccessToken,
  useListPersonalAccessTokens,
} from "../api/gen/endpoints/api-tokens/api-tokens";
import { useGetOrganization } from "../api/gen/endpoints/organization/organization";
import type { Permission, PersonalAccessToken, ServiceAccountToken, User } from "../api/gen/model";
import { problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { heldInOrder } from "./permission-picker";
import { NeverExpires, TokenCreateDialog } from "./token-created-dialog";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";

type AnyToken = PersonalAccessToken | ServiceAccountToken;

function Expiry({ token }: { token: AnyToken }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const [now] = useState(() => Date.now());
  if (!token.expires_at) {
    return <NeverExpires />;
  }
  const past = new Date(token.expires_at).getTime() <= now;
  return (
    <span className={past ? "text-destructive" : "text-muted-foreground"}>
      {t(past ? "tokens.list.expired" : "tokens.list.expires", {
        time: dateTime(token.expires_at),
      })}
    </span>
  );
}

function LastUse({ token }: { token: AnyToken }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  if (!token.last_used_at) {
    return <span className="text-muted-foreground">{t("tokens.list.neverUsed")}</span>;
  }
  const time = dateTime(token.last_used_at);
  return (
    <span className="break-words text-muted-foreground" data-testid="token-last-use">
      {token.last_used_address
        ? t("tokens.list.lastUsedFrom", { time, address: token.last_used_address })
        : t("tokens.list.lastUsed", { time })}
    </span>
  );
}

/** Asks before a token is revoked; scripts that use it stop working at once. */
function RevokeDialog<T extends AnyToken>({
  token,
  onOpenChange,
  revoke,
}: {
  token: T | null;
  onOpenChange: (open: boolean) => void;
  revoke: { pending: boolean; error: unknown; run: (token: T) => void };
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={token !== null} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle className="break-words">
            {t("tokens.revoke.title", { name: token?.name ?? "" })}
          </DialogTitle>
          <DialogDescription>{t("tokens.revoke.confirm")}</DialogDescription>
        </DialogHeader>
        {revoke.error !== null && revoke.error !== undefined && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {problemText(t, revoke.error)}
            </AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
          <Button
            variant="destructive"
            disabled={revoke.pending}
            onClick={() => {
              if (token !== null) {
                revoke.run(token);
              }
            }}
          >
            {t("tokens.revoke.action")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * The tokens of a user or a Service account, never with their values. With revoke, each row has "Revoke", which asks
 * first; onRevoked reads the list again.
 */
export function TokenList<T extends AnyToken>({
  label,
  tokens,
  loading,
  error,
  empty,
  revoke,
  onRevoked,
}: {
  label: string;
  tokens: readonly T[];
  loading: boolean;
  error: unknown;
  empty: string;
  revoke?: (token: T) => Promise<void>;
  onRevoked: () => void;
}) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const [asking, setAsking] = useState<T | null>(null);
  const run = useMutation({
    mutationFn: (token: T) => (revoke ? revoke(token) : Promise.resolve()),
    onSuccess: () => {
      setAsking(null);
      onRevoked();
    },
  });
  if (tokens.length === 0) {
    return (
      <p className="text-sm text-muted-foreground" role="status">
        {loading ? t("common.loading") : error ? problemText(t, error) : empty}
      </p>
    );
  }
  return (
    <>
      <ul className="flex flex-col divide-y rounded-lg border" aria-label={label}>
        {tokens.map((token) => (
          <li
            key={token.id}
            className="flex flex-col gap-2 px-3 py-2.5 text-sm sm:flex-row sm:items-start"
            data-testid="token-row"
          >
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <span className="font-medium break-words" data-testid="token-name">
                {token.name}
              </span>
              {"permissions" in token && (
                <ul className="flex flex-wrap gap-1" aria-label={t("tokens.fields.permissions")}>
                  {heldInOrder(token.permissions).map((p: Permission) => (
                    <li
                      key={p}
                      className="rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs break-all"
                    >
                      {p}
                    </li>
                  ))}
                </ul>
              )}
              <Expiry token={token} />
              <LastUse token={token} />
              <span className="text-muted-foreground">
                {t("tokens.list.created", { time: dateTime(token.created_at) })}
              </span>
            </div>
            {revoke !== undefined && (
              <Button
                variant="outline"
                size="sm"
                className="self-start"
                aria-label={t("tokens.revoke.label", { name: token.name })}
                onClick={() => {
                  run.reset();
                  setAsking(token);
                }}
              >
                {t("tokens.revoke.action")}
              </Button>
            )}
          </li>
        ))}
      </ul>
      {error !== null && error !== undefined && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, error)}
        </p>
      )}
      <RevokeDialog
        token={asking}
        onOpenChange={(open) => {
          if (!open) {
            setAsking(null);
          }
        }}
        revoke={{ pending: run.isPending, error: run.error, run: (token) => run.mutate(token) }}
      />
    </>
  );
}

/**
 * For an account that signs in through OIDC without an offline token, the tokens stop working once the last OIDC
 * sign-in is older than auth.oidc_token_grace (C-04.FR-8); the page shows when, from the last sign-in.
 */
function OidcGrace({ user }: { user: User }) {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const [now] = useState(() => Date.now());
  const applies =
    user.sign_in_method === "oidc" && user.oidc_offline_access === false && !!user.last_sign_in_at;
  const org = useGetOrganization({ query: { enabled: applies } });
  const grace = org.data?.oidc_token_grace_seconds;
  if (!applies || !user.last_sign_in_at || grace === undefined) {
    return null;
  }
  const until = new Date(user.last_sign_in_at).getTime() + grace * 1000;
  const time = dateTime(new Date(until).toISOString());
  return (
    <Alert data-testid="token-grace">
      <AlertDescription className="text-current">
        {until > now ? t("tokens.oidcGrace", { time }) : t("tokens.oidcGraceOver", { time })}
      </AlertDescription>
    </Alert>
  );
}

export function ProfileTokens({ user, held }: { user: User; held: readonly Permission[] }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const list = useListPersonalAccessTokens();
  const refresh = () =>
    void queryClient.invalidateQueries({ queryKey: getListPersonalAccessTokensQueryKey() });
  return (
    <Card className="lg:col-span-2">
      <CardHeader>
        <CardTitle>
          <h2>{t("tokens.personal.title")}</h2>
        </CardTitle>
        <CardDescription>{t("tokens.personal.hint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <OidcGrace user={user} />
        <div>
          <TokenCreateDialog
            description={t("tokens.personal.createHint")}
            held={held}
            create={(input) => createPersonalAccessToken(input)}
            onCreated={refresh}
          />
        </div>
        <TokenList
          label={t("tokens.personal.title")}
          tokens={list.data?.items ?? []}
          loading={list.isPending}
          error={list.error}
          empty={t("tokens.personal.empty")}
          revoke={(token) => revokePersonalAccessToken(token.id)}
          onRevoked={refresh}
        />
      </CardContent>
    </Card>
  );
}
