// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Organization → Link rules (C-12.FR-9): the rules with their name, scope and Matchers, the built-in "Explore" marked
// "Built-in", and "Create rule" with link-rules:write.

import { Link, createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { getListLinkRulesQueryKey, listLinkRules } from "../api/gen/endpoints/links/links";
import type { LinkRule } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { type DataColumn, DataTable, useCursorList } from "../components/data-table";
import { BuiltinBadge, scopeText } from "../components/link-rule-form";
import { matcherText } from "../components/matcher-builder";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/admin/organization/link-rules/")({
  staticData: { shell: true },
  component: LinkRulesPage,
});

function NameCell({ row }: { row: LinkRule }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Link
        to="/admin/organization/link-rules/$linkRuleId"
        params={{ linkRuleId: row.id }}
        className="font-medium wrap-anywhere text-primary underline-offset-4 hover:underline focus-visible:underline"
      >
        {row.name}
      </Link>
      {row.builtin && <BuiltinBadge />}
    </div>
  );
}

function MatchersCell({ row }: { row: LinkRule }) {
  const { t } = useTranslation();
  if (row.matchers.length === 0) {
    return <span className="text-muted-foreground">{t("linkRules.list.noMatchers")}</span>;
  }
  return (
    <ul className="flex flex-wrap gap-1" aria-label={t("linkRules.fields.matchers")}>
      {row.matchers.map((m) => (
        <li
          key={matcherText(m)}
          className="max-w-full rounded-md bg-muted px-1.5 py-0.5 font-mono text-xs wrap-anywhere"
        >
          {matcherText(m)}
        </li>
      ))}
    </ul>
  );
}

function LinkRulesTable() {
  const { t } = useTranslation();
  const list = useCursorList<LinkRule>(getListLinkRulesQueryKey(), (cursor, signal) =>
    listLinkRules({ cursor }, { signal }),
  );
  const columns = useMemo(
    (): DataColumn<LinkRule>[] => [
      { id: "name", header: t("linkRules.fields.name"), className: "min-w-36", Cell: NameCell },
      { id: "scope", header: t("linkRules.fields.scope"), text: (row) => scopeText(t, row.scope) },
      { id: "matchers", header: t("linkRules.fields.matchers"), Cell: MatchersCell },
    ],
    [t],
  );
  return (
    <DataTable
      label={t("linkRules.title")}
      columns={columns}
      list={list}
      rowId={(row) => row.id}
      empty={t("linkRules.list.empty")}
    />
  );
}

function LinkRulesPage() {
  const { t } = useTranslation();
  const canWrite = useCan("link-rules:write");
  return (
    <RequirePermission permission="link-rules:read">
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-col gap-1">
            <h1 className="text-2xl font-semibold tracking-tight">{t("linkRules.title")}</h1>
            <p className="text-muted-foreground">{t("linkRules.hint")}</p>
          </div>
          {canWrite && (
            <Link to="/admin/organization/link-rules/new" className={buttonVariants()}>
              {t("linkRules.create.start")}
            </Link>
          )}
        </div>
        <LinkRulesTable />
      </div>
    </RequirePermission>
  );
}
