// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

import { Outlet, createRootRoute } from "@tanstack/react-router";

export const Route = createRootRoute({
  component: RootLayout,
});

function RootLayout() {
  return <Outlet />;
}
