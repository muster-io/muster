// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The sessions of the profile (C-03.FR-9, FR-12): every session of the user, the current one marked, and "Sign out
// everywhere", which ends them all, this one included.

import { useState } from "react";
import { useTranslation } from "react-i18next";

import { useDeleteMySessions, useListMySessions } from "../api/gen/endpoints/profile/profile";
import { problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
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

export function ProfileSessions() {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const sessions = useListMySessions();
  const [confirming, setConfirming] = useState(false);
  const signOutAll = useDeleteMySessions({
    mutation: { onSuccess: () => window.location.assign("/sign-in") },
  });
  const items = sessions.data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{t("profile.sessions.title")}</h2>
        </CardTitle>
        <CardDescription>{t("profile.sessions.count", { count: items.length })}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <ul className="flex flex-col divide-y rounded-lg border">
          {items.map((s) => (
            <li key={s.id} className="flex flex-col gap-0.5 px-3 py-2 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium">
                  {s.method === "oidc" ? t("profile.method.oidc") : t("profile.method.local")}
                </span>
                {s.current && (
                  <span className="rounded-md bg-accent px-1.5 py-0.5 text-xs font-medium text-accent-foreground">
                    {t("profile.sessions.current")}
                  </span>
                )}
              </div>
              <span className="text-muted-foreground">
                {t("profile.sessions.times", {
                  created: dateTime(s.created_at),
                  used: dateTime(s.last_used_at),
                })}
              </span>
              {(s.address || s.user_agent) && (
                <span className="break-all text-muted-foreground">
                  {[s.address, s.user_agent].filter(Boolean).join(" · ")}
                </span>
              )}
            </li>
          ))}
        </ul>
        {signOutAll.isError && (
          <Alert variant="destructive">
            <AlertDescription className="text-current">
              {problemText(t, signOutAll.error)}
            </AlertDescription>
          </Alert>
        )}
        <div>
          <Button variant="outline" onClick={() => setConfirming(true)}>
            {t("profile.sessions.signOutEverywhere")}
          </Button>
        </div>
        <Dialog open={confirming} onOpenChange={setConfirming}>
          <DialogContent closeLabel={t("common.close")}>
            <DialogHeader>
              <DialogTitle>{t("profile.sessions.signOutEverywhere")}</DialogTitle>
              <DialogDescription>{t("profile.sessions.confirm")}</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
              <Button
                variant="destructive"
                disabled={signOutAll.isPending}
                onClick={() => signOutAll.mutate()}
              >
                {t("profile.sessions.signOutEverywhere")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}
