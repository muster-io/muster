// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The warning of the sign-in page when the page is not a secure context: the session cookie is Secure, so on plain
// http to any host but localhost the browser drops it and every sign-in returns to the sign-in page. The form stays
// usable; the warning only says why it cannot work and what to open instead.

import { useTranslation } from "react-i18next";

import { Alert, AlertDescription, AlertTitle } from "./ui/alert";

export function InsecureContextAlert() {
  const { t } = useTranslation();
  if (window.isSecureContext) {
    return null;
  }
  return (
    <Alert variant="destructive">
      <AlertTitle>{t("signIn.insecure.title")}</AlertTitle>
      <AlertDescription className="text-current">
        {t("signIn.insecure.description")}
      </AlertDescription>
    </Alert>
  );
}
