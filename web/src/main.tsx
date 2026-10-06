// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for Vite to bundle it
import "./styles.css";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { I18nextProvider } from "react-i18next";

import i18n from "./i18n";
import { isApiError } from "./lib/api";
import { routeTree } from "./routeTree.gen";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // A refusal does not change on its own; only network failures and server errors are tried again.
      retry: (failures, err) => failures < 2 && !(isApiError(err) && err.status < 500),
    },
  },
});

const router = createRouter({ routeTree, context: { queryClient }, defaultPreload: false });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

const rootElement = document.getElementById("root");
if (rootElement === null) {
  throw new Error("index.html has no #root element");
}

createRoot(rootElement).render(
  <StrictMode>
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>
  </StrictMode>,
);
