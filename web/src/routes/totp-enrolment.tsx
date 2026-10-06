// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The TOTP enrolment that the policy "TOTP required" demands right after sign-in (C-03.FR-10, C-03.AC-2): the root
// route leads every address of a session in the state totp_enrolment_required here until the enrolment is confirmed.

import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { AuthLayout } from "../components/app-shell";
import { RecoveryCodes, TotpEnrolment } from "../components/profile-totp";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { signOut } from "../components/user-menu";
import { SESSION_QUERY_KEY, safeReturnTo } from "../lib/api";

const searchSchema = z.object({ return_to: z.string().optional().catch(undefined) });

export const Route = createFileRoute("/totp-enrolment")({
  validateSearch: searchSchema,
  component: EnrolmentPage,
});

function EnrolmentPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const returnTo = safeReturnTo(Route.useSearch().return_to);
  const [codes, setCodes] = useState<string[] | undefined>();
  return (
    <AuthLayout wide>
      <Card>
        <CardHeader>
          <CardTitle>
            <h1 className="text-lg">{t("totp.enrolmentPage.title")}</h1>
          </CardTitle>
          {codes === undefined && (
            <CardDescription>{t("totp.enrolmentPage.required")}</CardDescription>
          )}
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {codes === undefined ? (
            <TotpEnrolment onEnrolled={setCodes} />
          ) : (
            <RecoveryCodes
              codes={codes}
              onContinue={() => {
                void queryClient.invalidateQueries({ queryKey: SESSION_QUERY_KEY });
                void navigate({ href: returnTo ?? "/", replace: true });
              }}
            />
          )}
          {codes === undefined && (
            <div>
              <Button variant="link" className="px-0" onClick={() => void signOut()}>
                {t("userMenu.signOut")}
              </Button>
            </div>
          )}
        </CardContent>
      </Card>
    </AuthLayout>
  );
}
