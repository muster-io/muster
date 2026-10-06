// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The root route reads the session on every navigation and sends each address where the session may go: without a
// session to the sign-in page, a limited session only to its second factor or its enrolment (C-03.AC-2), a signed-in
// one away from the sign-in pages. Pages that set staticData.shell live in the application shell.

import type { QueryClient } from "@tanstack/react-query";
import {
  Link,
  Outlet,
  createRootRouteWithContext,
  redirect,
  useMatches,
  useRouter,
} from "@tanstack/react-router";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";

import { AppShell } from "../components/app-shell";
import { Button, buttonVariants } from "../components/ui/button";
import { applyLanguage } from "../i18n";
import {
  SESSION_ENDED,
  type SessionRead,
  safeReturnTo,
  sessionQuery,
  signInHref,
  useSession,
  withReturnTo,
} from "../lib/api";

export interface RouterContext {
  queryClient: QueryClient;
}

declare module "@tanstack/react-router" {
  interface StaticDataRouteOption {
    /** The page lives in the application shell. */
    shell?: boolean;
  }
}

/** Where an address leads for the session read, or undefined when the session may open it. */
export function guardTarget(
  pathname: string,
  search: string,
  read: SessionRead,
): string | undefined {
  const session = read.session;
  const returnTo = safeReturnTo(new URLSearchParams(search).get("return_to"));
  if (pathname === "/password-setup") {
    return undefined;
  }
  if (session === null) {
    return pathname === "/sign-in"
      ? undefined
      : signInHref(read.ended ? SESSION_ENDED : undefined, pathname, search);
  }
  if (session.state === "totp_required") {
    return pathname === "/sign-in/totp"
      ? undefined
      : withReturnTo("/sign-in/totp", pathname === "/sign-in" ? returnTo : undefined);
  }
  if (session.state === "totp_enrolment_required") {
    return pathname === "/totp-enrolment"
      ? undefined
      : withReturnTo("/totp-enrolment", pathname === "/sign-in" ? returnTo : undefined);
  }
  const limitedPages = ["/sign-in", "/sign-in/totp", "/totp-enrolment"];
  return limitedPages.includes(pathname) ? (returnTo ?? "/") : undefined;
}

export const Route = createRootRouteWithContext<RouterContext>()({
  beforeLoad: async ({ context, location }) => {
    if (location.pathname === "/password-setup") {
      return;
    }
    const read = await context.queryClient.fetchQuery(sessionQuery);
    const target = guardTarget(location.pathname, location.searchStr, read);
    if (target !== undefined) {
      throw redirect({ href: target, replace: true });
    }
  },
  component: RootLayout,
  notFoundComponent: NotFound,
  errorComponent: ErrorPage,
});

function LanguageSync() {
  const session = useSession();
  const language = session?.user.language;
  useEffect(() => {
    applyLanguage(language);
  }, [language]);
  return null;
}

function RootLayout() {
  const shell = useMatches({
    select: (matches) => matches.some((m) => m.staticData.shell === true),
  });
  return (
    <>
      <LanguageSync />
      {shell ? (
        <AppShell>
          <Outlet />
        </AppShell>
      ) : (
        <Outlet />
      )}
    </>
  );
}

function NotFound() {
  const { t } = useTranslation();
  return (
    <main className="mx-auto flex max-w-md flex-col items-start gap-4 px-4 py-16">
      <h1 className="text-xl font-semibold">{t("errors.notFoundTitle")}</h1>
      <p className="text-muted-foreground">{t("errors.notFound")}</p>
      <Link to="/" className={buttonVariants()}>
        {t("errors.goHome")}
      </Link>
    </main>
  );
}

function ErrorPage({ reset }: { reset: () => void }) {
  const { t } = useTranslation();
  const router = useRouter();
  return (
    <main className="mx-auto flex max-w-md flex-col items-start gap-4 px-4 py-16">
      <h1 className="text-xl font-semibold">{t("errors.unexpectedTitle")}</h1>
      <p className="text-muted-foreground">{t("errors.unexpected")}</p>
      <Button
        onClick={() => {
          reset();
          void router.invalidate();
        }}
      >
        {t("errors.tryAgain")}
      </Button>
    </main>
  );
}
