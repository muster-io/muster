// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Home page (C-27.FR-9): the page the logo leads to and the one signing in lands on. It has no entry in
// the main navigation. It shows "I am an Assignee" (C-32.FR-18), the Alert Groups an Escalation chain Paged the signed-in
// User about, and "On call now"; a dashboard follows in a later story. The Alert Group list stays at /alert-groups.

import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { useCan } from "../components/app-shell";
import { IAmAnAssigneeSection } from "../components/i-am-an-assignee";
import { OnCallNowSection } from "../components/on-call-now";

export const Route = createFileRoute("/")({
  staticData: { shell: true },
  component: HomePage,
});

function HomePage() {
  const { t } = useTranslation();
  const canSchedules = useCan("schedules:read");
  const canAlertGroups = useCan("alert-groups:read");
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <h1 className="text-2xl font-semibold tracking-tight">{t("home.title")}</h1>
      {canAlertGroups && <IAmAnAssigneeSection />}
      {canSchedules && <OnCallNowSection />}
    </div>
  );
}
