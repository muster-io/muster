// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Link rule (C-12.FR-9, AC-6): read-only without link-rules:write, else its form and "Delete", except for the
// built-in "Explore" rule, which has no "Delete". The form keeps the version it was read at and sends it as If-Match.
// When the rule changes elsewhere an untouched form takes the new version and says so; a form with changes keeps
// them, and its save is refused (412) with "Someone else changed this Link rule. Reload to see the changes."

import { useQueryClient } from "@tanstack/react-query";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getGetLinkRuleQueryKey,
  getGetLinkRuleQueryOptions,
  getListLinkRulesQueryKey,
  updateLinkRule,
  useGetLinkRule,
} from "../api/gen/endpoints/links/links";
import type { LinkRule } from "../api/gen/model";
import { RequirePermission, useCan } from "../components/app-shell";
import { LinkRuleDeleteDialog, LinkRuleForm } from "../components/link-rule-form";
import { Alert, AlertDescription } from "../components/ui/alert";
import { buttonVariants } from "../components/ui/button";
import { problemText } from "../lib/api";

export const Route = createFileRoute("/admin/organization/link-rules/$linkRuleId")({
  staticData: { shell: true },
  component: LinkRulePage,
});

const LIST = { to: "/admin/organization/link-rules" } as const;

function EditForm({ current }: { current: LinkRule }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const canWrite = useCan("link-rules:write");
  const [base, setBase] = useState(current);
  const [dirty, setDirty] = useState(false);
  const [replaced, setReplaced] = useState(false);
  const [reloadError, setReloadError] = useState<unknown>(null);
  // A newer version (another Admin, another tab) replaces an untouched form, which says so.
  if (current.etag !== base.etag && !dirty) {
    setBase(current);
    setReplaced(true);
  }
  return (
    <>
      {replaced && (
        <Alert data-testid="link-rule-replaced">
          <AlertDescription>{t("linkRules.errors.changedElsewhere")}</AlertDescription>
        </Alert>
      )}
      {reloadError !== null && (
        <Alert variant="destructive">
          <AlertDescription className="text-current">
            {problemText(t, reloadError)}
          </AlertDescription>
        </Alert>
      )}
      <LinkRuleForm
        key={base.etag}
        rule={base}
        readOnly={!canWrite}
        submitLabel={t("common.save")}
        stale={current.etag !== base.etag}
        onDirtyChange={setDirty}
        save={async (input) => {
          await queryClient.cancelQueries({ queryKey: getGetLinkRuleQueryKey(base.id) });
          const updated = await updateLinkRule(base.id, input, {
            headers: { "If-Match": base.etag ?? "" },
          });
          // The page leaves first, so that the form never takes its own save for a change made elsewhere.
          await navigate(LIST);
          queryClient.setQueryData(getGetLinkRuleQueryKey(updated.id), updated);
          void queryClient.invalidateQueries({ queryKey: getListLinkRulesQueryKey() });
        }}
        onReload={() => {
          setReloadError(null);
          queryClient
            .fetchQuery({ ...getGetLinkRuleQueryOptions(base.id), staleTime: 0 })
            .then((fresh) => {
              setDirty(false);
              setReplaced(false);
              setBase(fresh);
            })
            .catch((err: unknown) => setReloadError(err));
        }}
        onCancel={() => void navigate(LIST)}
      />
    </>
  );
}

function LinkRuleView({ linkRuleId }: { linkRuleId: string }) {
  const { t } = useTranslation();
  const canWrite = useCan("link-rules:write");
  const query = useGetLinkRule(linkRuleId);
  const rule = query.data;
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Link to={LIST.to} className={buttonVariants({ variant: "link", className: "w-fit px-0" })}>
          <ArrowLeftIcon aria-hidden="true" />
          {t("linkRules.title")}
        </Link>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <h1 className="min-w-0 text-2xl font-semibold tracking-tight wrap-anywhere">
            {rule?.name ?? t("linkRules.edit.title")}
          </h1>
          {/* Outside the form, which a newer version replaces; Delete sends the version read last. */}
          {canWrite && rule !== undefined && <LinkRuleDeleteDialog rule={rule} />}
        </div>
      </div>
      {rule === undefined ? (
        <p className="text-sm text-muted-foreground" role="status">
          {query.isError ? problemText(t, query.error) : t("common.loading")}
        </p>
      ) : (
        <EditForm current={rule} />
      )}
    </div>
  );
}

function LinkRulePage() {
  const { linkRuleId } = Route.useParams();
  return (
    <RequirePermission permission="link-rules:read">
      <LinkRuleView linkRuleId={linkRuleId} />
    </RequirePermission>
  );
}
