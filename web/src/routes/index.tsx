// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The home page is the Alert Group list (C-09.FR-13): "/" leads there.

import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/")({
  beforeLoad: () => {
    throw redirect({ to: "/alert-groups", replace: true });
  },
});
