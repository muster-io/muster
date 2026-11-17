// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "iCal subscription" of the Schedule page (C-27.FR-10, C-27.AC-7): the caller's personal link for a calendar
// application. "New link" issues it and shows it once, from the answer, with "Copy the link now: it will not be shown
// again"; the SPA keeps it in the memory of the page only, so a reload shows no link, just whether one exists. Issuing a
// link while one exists asks first, because the old one stops working at once. "Revoke" ends the link.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { TriangleAlertIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getGetMyIcalFeedQueryKey,
  getMyIcalFeed,
  issueMyIcalFeed,
  revokeMyIcalFeed,
} from "../api/gen/endpoints/profile/profile";
import type { IcalFeed, IcalFeedIssued } from "../api/gen/model";
import { problemText } from "../lib/api";
import { useTimeFormat } from "../lib/time";
import { DocsLink } from "./docs-link";
import { CopyBlock } from "./integration-token-dialog";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";

export function IcalSubscription() {
  const { t } = useTranslation();
  const { dateTime } = useTimeFormat();
  const queryClient = useQueryClient();
  const key = getGetMyIcalFeedQueryKey();
  const feed = useQuery({ queryKey: key, queryFn: ({ signal }) => getMyIcalFeed({ signal }) });
  // The link lives here and nowhere else: not in the query cache, not in storage.
  const [issued, setIssued] = useState<IcalFeedIssued | null>(null);
  const [confirming, setConfirming] = useState(false);
  const issue = useMutation({
    mutationFn: () => issueMyIcalFeed(),
    // The answer holds the link: the mutation cache drops it as soon as the page stops observing it.
    gcTime: 0,
    onSuccess: (link) => {
      setIssued(link);
      setConfirming(false);
      queryClient.setQueryData<IcalFeed>(key, { exists: true, issued_at: link.issued_at });
    },
  });
  const revoke = useMutation({
    mutationFn: () => revokeMyIcalFeed(),
    onSuccess: () => {
      setIssued(null);
      issue.reset();
      queryClient.setQueryData<IcalFeed>(key, { exists: false, issued_at: null });
    },
  });
  const exists = feed.data?.exists === true;
  const error = feed.isError ? feed.error : issue.isError ? issue.error : revoke.error;
  return (
    <section
      className="flex min-w-0 flex-col gap-3"
      aria-labelledby="ical-title"
      data-testid="ical-subscription"
    >
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-baseline gap-x-3">
          <h2 id="ical-title" className="text-lg font-semibold">
            {t("ical.title")}
          </h2>
          <DocsLink to="field.ical" kind="learn_more" about={t("ical.title")} />
        </div>
        <p className="text-sm text-muted-foreground">{t("ical.hint")}</p>
      </div>
      {feed.data === undefined && !feed.isError ? (
        <p className="text-sm text-muted-foreground" role="status">
          {t("common.loading")}
        </p>
      ) : (
        <>
          {issued !== null ? (
            <div className="flex min-w-0 flex-col gap-2" role="status" data-testid="ical-link">
              <CopyBlock
                id="ical-link"
                label={t("ical.link")}
                text={issued.url}
                testId="ical-url"
              />
              <p className="flex items-start gap-1.5 text-sm font-medium">
                <TriangleAlertIcon
                  aria-hidden="true"
                  className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
                />
                <span>{t("ical.copyNow")}</span>
              </p>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground" data-testid="ical-state">
              {exists && feed.data?.issued_at != null
                ? t("ical.issued", { time: dateTime(feed.data.issued_at) })
                : t("ical.none")}
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              disabled={issue.isPending}
              onClick={() => (exists ? setConfirming(true) : issue.mutate())}
            >
              {t("ical.newLink")}
            </Button>
            {exists && (
              <Button
                type="button"
                variant="outline"
                disabled={revoke.isPending}
                onClick={() => revoke.mutate()}
              >
                {t("ical.revoke")}
              </Button>
            )}
          </div>
        </>
      )}
      {error !== null && error !== undefined && (
        <p className="text-sm text-destructive" role="alert">
          {problemText(t, error)}
        </p>
      )}
      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("ical.replaceTitle")}</DialogTitle>
            <DialogDescription>{t("ical.replaceDescription")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>{t("common.cancel")}</DialogClose>
            <Button type="button" disabled={issue.isPending} onClick={() => issue.mutate()}>
              {t("ical.replaceConfirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
