// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// "Create rule" (C-12.FR-9): the form of a new Link rule; the list opens after it is created.

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  createLinkRule,
  getGetLinkRuleQueryKey,
  getListLinkRulesQueryKey,
} from "../api/gen/endpoints/links/links";
import { RequirePermission } from "../components/app-shell";
import { LinkRuleForm } from "../components/link-rule-form";
import { buttonVariants } from "../components/ui/button";

export const Route = createFileRoute("/admin/organization/link-rules/new")({
  staticData: { shell: true },
  component: NewLinkRulePage,
});

function NewLinkRule() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link
          to="/admin/organization/link-rules"
          className={buttonVariants({ variant: "link", className: "w-fit px-0" })}
        >
          <ArrowLeftIcon aria-hidden="true" />
          {t("linkRules.title")}
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight">{t("linkRules.create.title")}</h1>
      </div>
      <LinkRuleForm
        submitLabel={t("linkRules.create.submit")}
        save={async (input) => {
          const created = await createLinkRule(input);
          queryClient.setQueryData(getGetLinkRuleQueryKey(created.id), created);
          void queryClient.invalidateQueries({ queryKey: getListLinkRulesQueryKey() });
          await navigate({ to: "/admin/organization/link-rules" });
        }}
        onCancel={() => void navigate({ to: "/admin/organization/link-rules" })}
      />
    </div>
  );
}

function NewLinkRulePage() {
  return (
    <RequirePermission permission="link-rules:write">
      <NewLinkRule />
    </RequirePermission>
  );
}
