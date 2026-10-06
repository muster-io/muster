// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The home page; the Alert Group list replaces this placeholder.

import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { useSession } from "../lib/api";

export const Route = createFileRoute("/")({
  staticData: { shell: true },
  component: HomePage,
});

function HomePage() {
  const { t } = useTranslation();
  const session = useSession();
  return (
    <section className="flex flex-col gap-2">
      <h1 className="text-2xl font-semibold tracking-tight">
        {t("home.title", { name: session?.user.name ?? "" })}
      </h1>
      <p className="text-muted-foreground">{t("home.placeholder")}</p>
    </section>
  );
}
