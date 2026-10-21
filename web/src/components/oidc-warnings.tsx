// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The warnings of the OIDC settings and of "Check connection" (C-03.FR-5, FR-6), each with the guide that says how to
// fix it: nobody can sign in, the groups claim not seen in a sign-in yet, the client secret expiring and the last
// active Admin kept by Role sync.

import type { TFunction } from "i18next";
import { TriangleAlertIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { OidcWarning } from "../api/gen/model";
import { useTimeFormat } from "../lib/time";
import { DocsLink } from "./docs-link";
import { roleLabel } from "./user-create-dialog";

function WarningAlert({
  children,
  fix,
  testId,
}: {
  children: ReactNode;
  /** The "How to fix" link after the text. */
  fix?: ReactNode;
  testId: string;
}) {
  return (
    <div
      className="flex items-start gap-2 rounded-lg border border-warning/60 bg-warning-surface px-3 py-2 text-sm"
      data-testid={testId}
    >
      <TriangleAlertIcon
        aria-hidden="true"
        className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-warning"
      />
      <p>
        <span>{children}</span>
        {fix !== undefined && <> {fix}</>}
      </p>
    </div>
  );
}

/** The text of a warning and the guide that says how to fix it, if one does. */
interface WarningShown {
  text: string;
  fix?: ReactNode;
}

function warningShown(
  t: TFunction,
  w: OidcWarning,
  groupsClaim: string,
  date: (d: string) => string,
): WarningShown | null {
  switch (w.kind) {
    case "nobody_can_sign_in":
      return {
        text: t("oidc.warnings.nobodyCanSignIn"),
        fix: (
          <DocsLink
            to="fix.oidc_mapping"
            kind="how_to_fix"
            about={t("docs.fixes.oidcMapping")}
          />
        ),
      };
    case "groups_claim_missing":
      return {
        text: t("oidc.warnings.groupsClaimMissing", { claim: groupsClaim }),
        fix: (
          <DocsLink
            to="fix.oidc_mapping"
            kind="how_to_fix"
            about={t("docs.fixes.oidcGroupsClaim")}
          />
        ),
      };
    case "secret_expiring":
      return w.expires_on
        ? {
            text: t("oidc.warnings.secretExpiring", { date: date(w.expires_on) }),
            fix: (
              <DocsLink
                to="fix.oidc_secret"
                kind="how_to_fix"
                about={t("docs.fixes.oidcSecret")}
              />
            ),
          }
        : null;
    case "last_admin_kept":
      // Its own text says what to do, in Muster: no guide (reference.md, Documentation links).
      return w.user && w.role
        ? {
            text: t("oidc.warnings.lastAdminKept", {
              user: w.user.name,
              role: roleLabel(t, w.role),
            }),
          }
        : null;
    default:
      return null;
  }
}

/** The warnings of the settings, each text once; groupsClaim is the saved groups claim, which a warning names. */
export function OidcWarnings({
  warnings,
  groupsClaim,
}: {
  warnings: readonly OidcWarning[];
  groupsClaim: string;
}) {
  const { t } = useTranslation();
  const { date } = useTimeFormat();
  const shown = new Map<string, WarningShown>();
  for (const w of warnings) {
    const one = warningShown(t, w, groupsClaim, date);
    if (one !== null && !shown.has(one.text)) {
      shown.set(one.text, one);
    }
  }
  return (
    <div role="status" aria-live="polite" className="flex flex-col gap-2 empty:hidden">
      {[...shown.values()].map(({ text, fix }) => (
        <WarningAlert key={text} testId="oidc-warning" fix={fix}>
          {text}
        </WarningAlert>
      ))}
    </div>
  );
}
