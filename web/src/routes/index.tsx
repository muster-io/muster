// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Home page (C-27.FR-9): the page the logo leads to and the one signing in lands on. It has no entry in
// the main navigation. For now it shows "On call now"; "I am an Assignee" and a dashboard follow in later stories.
// The Alert Group list stays at /alert-groups.

import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { useCan } from "../components/app-shell";
import { OnCallNowSection } from "../components/on-call-now";

export const Route = createFileRoute("/")({
  staticData: { shell: true },
  component: HomePage,
});

function HomePage() {
  const { t } = useTranslation();
  const canSchedules = useCan("schedules:read");
  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-2xl font-semibold tracking-tight">{t("home.title")}</h1>
      {canSchedules && <OnCallNowSection />}
    </div>
  );
}
