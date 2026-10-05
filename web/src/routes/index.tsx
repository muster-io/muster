// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/")({
  component: IndexPage,
});

function IndexPage() {
  return (
    <main>
      <h1>Muster</h1>
    </main>
  );
}
