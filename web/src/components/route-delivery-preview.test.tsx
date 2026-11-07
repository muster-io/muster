// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The preview "Where alerts go" of the Route editor in a real browser, against a stubbed API (C-26.FR-12, AC-9): it asks
// with the unsaved settings, after a pause and once for a burst of changes; it lists the Alert Groups with their Teams
// and the Destinations each reaches — a Team's marked "via {Team}", the rise case with all three Destinations —, the
// number of Alert Groups per Destination and the hint that Alerts of different Teams share an Alert Group; and it
// shows its loading, empty, refused and failed states, in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  DeliveryPreview,
  DeliveryPreviewDestination,
  DeliveryPreviewGroup,
  DestinationRef,
  GroupKeyPreview,
  GroupKeyPreviewRequest,
} from "../api/gen/model";
import i18n from "../i18n";
import { teamRef } from "../test/fixtures/teams";
import {
  PREVIEW_PAUSE,
  RouteDeliveryPreview,
  type DeliveryPreviewQuery,
} from "./route-delivery-preview";

function ref(id: string, name: string): DestinationRef {
  return { id, name, type: "mattermost", health: { state: "healthy" } };
}

const INFRA_CRITICAL = ref("DS000000000IC", "infra-critical");
const INFRA_NOTICE = ref("DS000000000IN", "infra-notice");
const PAYMENTS_NOTICE = ref("DS000000000PN", "payments-notice");
const PAYMENTS = teamRef({ id: "TM0000000000PA", name: "payments", slug: "payments" });

function reached(
  destination: DestinationRef,
  entries: DeliveryPreviewDestination["entries"],
): DeliveryPreviewDestination {
  return { destination, entries };
}

const ROUTE_ANY = { kind: "route", team: null, severity_condition: "any" } as const;
const ROUTE_CRITICAL = { kind: "route", team: null, severity_condition: "critical" } as const;
const ROUTE_BELOW = { kind: "route", team: null, severity_condition: "below_critical" } as const;
const PAYMENTS_BELOW = {
  kind: "team",
  team: PAYMENTS,
  severity_condition: "below_critical",
} as const;

// payments' Alert Group started as a warning and rose to critical: it reaches all three Destinations.
const PAYMENTS_GROUP: DeliveryPreviewGroup = {
  group_key_values: { alertname: "KubeNodeNotReady", system: "k8s", team: "payments" },
  alert_count: 3,
  severity_level: "critical",
  teams: [PAYMENTS],
  destinations: [
    reached(INFRA_CRITICAL, [ROUTE_CRITICAL]),
    reached(INFRA_NOTICE, [ROUTE_BELOW]),
    reached(PAYMENTS_NOTICE, [PAYMENTS_BELOW]),
  ],
};
const DEVOPS_GROUP: DeliveryPreviewGroup = {
  group_key_values: { alertname: "KubeNodeNotReady", system: "k8s", team: "devops" },
  alert_count: 1,
  severity_level: "critical",
  teams: [],
  destinations: [reached(INFRA_CRITICAL, [ROUTE_CRITICAL])],
};

function deliveryOf(overrides: Partial<DeliveryPreview> = {}): DeliveryPreview {
  return {
    alert_groups: [PAYMENTS_GROUP, DEVOPS_GROUP],
    destination_counts: [
      { destination: INFRA_CRITICAL, alert_group_count: 2 },
      { destination: INFRA_NOTICE, alert_group_count: 1 },
      { destination: PAYMENTS_NOTICE, alert_group_count: 1 },
    ],
    shared_key_warning: false,
    ...overrides,
  };
}

function previewOf(delivery: DeliveryPreview | undefined, groups = 2): GroupKeyPreview {
  return {
    period_seconds: 86400,
    truncated: false,
    proposed: { alert_group_count: groups, examples: [] },
    delivery,
  };
}

const QUERY: DeliveryPreviewQuery = {
  matchers: [{ label: "category", op: "=", value: "infra" }],
  proposed_group_key: ["alertname", "system"],
  minimum_severity: "none",
  destination_entries: [
    { destination_id: INFRA_CRITICAL.id, severity_condition: "critical" },
    { destination_id: INFRA_NOTICE.id, severity_condition: "below_critical" },
  ],
  deliver_to_teams: true,
};

interface Server {
  bodies: GroupKeyPreviewRequest[];
  /** What the next request answers. */
  answer: () => Promise<Response>;
}

let server: Server;

function reply(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
  server = { bodies: [], answer: () => Promise.resolve(reply(200, previewOf(deliveryOf()))) };
  vi.spyOn(window, "fetch").mockImplementation((input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url === "/api/v1/group-key-previews" && init?.method === "POST") {
      const raw = init.body;
      const parsed: unknown = JSON.parse(typeof raw === "string" ? raw : "{}");
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the request is the preview request
      server.bodies.push(parsed as GroupKeyPreviewRequest);
      return server.answer();
    }
    return Promise.resolve(reply(404, {}));
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

function Harness({
  first = QUERY,
  enabled = true,
}: {
  first?: DeliveryPreviewQuery;
  enabled?: boolean;
}) {
  const [query, setQuery] = useState(first);
  return (
    <>
      <button type="button" onClick={() => setQuery({ ...query, minimum_severity: "warning" })}>
        warning
      </button>
      <button type="button" onClick={() => setQuery({ ...query, minimum_severity: "critical" })}>
        critical
      </button>
      <RouteDeliveryPreview query={query} enabled={enabled} teamLabel="team" />
    </>
  );
}

async function renderPreview(props: Parameters<typeof Harness>[0] = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <Harness {...props} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

const groups = () => page.getByTestId("delivery-group");

describe("the preview Where alerts go", () => {
  test("asks with the unsaved settings, and lists the Alert Groups with Teams and Destinations", async () => {
    await renderPreview();
    await expect.element(page.getByRole("heading", { name: "Where alerts go" })).toBeVisible();
    await expect.element(groups().first()).toBeVisible();
    expect(groups().elements()).toHaveLength(2);
    expect(server.bodies).toHaveLength(1);
    expect(server.bodies[0]).toEqual(QUERY);

    // payments: the Team, and all three Destinations of the rise case, the Team's marked "via payments".
    const payments = groups().nth(0);
    await expect
      .element(payments.getByTestId("delivery-group-teams"))
      .toHaveTextContent("Teams: payments");
    expect(
      payments
        .getByTestId("delivery-preview-destination")
        .elements()
        .map((e) => e.textContent?.trim()),
    ).toEqual(["infra-critical", "infra-notice", "payments-notice via payments"]);
    expect(payments.element().textContent).toContain("alertname=KubeNodeNotReady");
    expect(payments.element().textContent).toContain("3 alerts · critical");
    // devops has no Team and only the critical Destination.
    const devops = groups().nth(1);
    await expect
      .element(devops.getByTestId("delivery-group-teams"))
      .toHaveTextContent("Teams: no Team");
    expect(
      devops
        .getByTestId("delivery-preview-destination")
        .elements()
        .map((e) => e.textContent?.trim()),
    ).toEqual(["infra-critical"]);
    expect(devops.element().textContent).toContain("1 alert · critical");

    // How many of all the Alert Groups reach each Destination.
    expect(
      page
        .getByTestId("delivery-count")
        .elements()
        .map((e) => e.textContent?.trim()),
    ).toEqual([
      "infra-critical reached by 2 of 2",
      "infra-notice reached by 1 of 2",
      "payments-notice reached by 1 of 2",
    ]);
    expect(page.getByTestId("preview-shared-key-warning").elements()).toHaveLength(0);
  });

  test("marks a Destination that the Route and a Team both reach", async () => {
    server.answer = () =>
      Promise.resolve(
        reply(
          200,
          previewOf(
            deliveryOf({
              alert_groups: [
                {
                  ...PAYMENTS_GROUP,
                  destinations: [reached(INFRA_NOTICE, [ROUTE_ANY, PAYMENTS_BELOW])],
                },
              ],
            }),
          ),
        ),
      );
    await renderPreview();
    await expect
      .element(page.getByTestId("delivery-preview-via"))
      .toHaveTextContent("Route, via payments");
  });

  test("shows the hint that Teams share an Alert Group where the API warns", async () => {
    server.answer = () =>
      Promise.resolve(reply(200, previewOf(deliveryOf({ shared_key_warning: true }))));
    await renderPreview();
    await expect
      .element(page.getByTestId("preview-shared-key-warning"))
      .toHaveTextContent(
        "Alerts of different Teams with the same key share one Alert Group, shown in the channels of all of them. Add team to the key if each Team needs its own.",
      );
  });

  test("asks once, after a pause, for a burst of changes, and keeps the old list while it updates", async () => {
    await renderPreview();
    await expect.element(groups().first()).toBeVisible();
    expect(server.bodies).toHaveLength(1);
    let release: (() => void) | undefined;
    server.answer = () =>
      new Promise<Response>((resolve) => {
        release = () =>
          resolve(reply(200, previewOf(deliveryOf({ alert_groups: [DEVOPS_GROUP] }), 1)));
      });
    await page.getByRole("button", { name: "warning" }).click();
    await page.getByRole("button", { name: "critical" }).click();
    // Nothing is asked before the pause ends; the list stays.
    expect(server.bodies).toHaveLength(1);
    expect(groups().elements()).toHaveLength(2);
    await vi.waitFor(() => expect(server.bodies).toHaveLength(2), { timeout: PREVIEW_PAUSE * 10 });
    expect(server.bodies[1]?.minimum_severity).toBe("critical");
    await expect.element(page.getByText("Updating…")).toBeVisible();
    expect(groups().elements()).toHaveLength(2);
    release?.();
    await vi.waitFor(() => expect(groups().elements()).toHaveLength(1));
    expect(server.bodies).toHaveLength(2);
  });

  test("shows its loading state, then says when no recent Alerts would reach the Route", async () => {
    let release: (() => void) | undefined;
    server.answer = () =>
      new Promise<Response>((resolve) => {
        release = () =>
          resolve(
            reply(200, previewOf(deliveryOf({ alert_groups: [], destination_counts: [] }), 0)),
          );
      });
    await renderPreview();
    await expect.element(page.getByRole("status")).toHaveTextContent("Loading…");
    release?.();
    await expect
      .element(page.getByTestId("delivery-preview-empty"))
      .toHaveTextContent("No recent Alerts would reach this Route.");
  });

  test("asks to add a Destination when the answer has no delivery", async () => {
    server.answer = () => Promise.resolve(reply(200, previewOf(undefined)));
    await renderPreview({ first: { ...QUERY, destination_entries: [], deliver_to_teams: false } });
    await expect
      .element(page.getByTestId("delivery-preview-none"))
      .toHaveTextContent(
        "Add a Destination, or turn on delivery to the Teams from the label, to see where alerts go.",
      );
  });

  test("asks nothing for a new Route without a Matcher", async () => {
    await renderPreview({ enabled: false });
    await expect
      .element(page.getByTestId("delivery-preview-needs-matchers"))
      .toHaveTextContent("Add a Matcher to see where alerts go.");
    await new Promise((done) => setTimeout(done, PREVIEW_PAUSE + 200));
    expect(server.bodies).toHaveLength(0);
  });

  test("tells a refusal about the fields, and a failure with its reason", async () => {
    server.answer = () =>
      Promise.resolve(
        reply(422, {
          type: "about:blank",
          title: "Unprocessable",
          status: 422,
          errors: [{ pointer: "/matchers/0/value", code: "invalid_regex" }],
        }),
      );
    await renderPreview();
    await expect
      .element(page.getByRole("alert"))
      .toHaveTextContent("Fix the fields marked above to see the preview.");
  });

  test("keeps the list it has and shows the failure of a later refresh", async () => {
    await renderPreview();
    await expect.element(groups().first()).toBeVisible();
    server.answer = () =>
      Promise.resolve(reply(500, { type: "about:blank", title: "Internal", status: 500 }));
    await page.getByRole("button", { name: "warning" }).click();
    await expect.element(page.getByRole("alert")).toBeVisible();
    expect(groups().elements()).toHaveLength(2);
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    server.answer = () =>
      Promise.resolve(reply(200, previewOf(deliveryOf({ shared_key_warning: true }))));
    await renderPreview();
    await expect.element(page.getByRole("heading", { name: "Куда придут алерты" })).toBeVisible();
    await expect
      .element(groups().nth(0).getByTestId("delivery-group-teams"))
      .toHaveTextContent("Команды: payments");
    await expect
      .element(groups().nth(0).getByTestId("delivery-preview-via"))
      .toHaveTextContent("через payments");
    expect(groups().nth(0).element().textContent).toContain("3 алерта · critical");
    await expect.element(page.getByTestId("preview-shared-key-warning")).toBeVisible();
    await expect
      .element(page.getByTestId("delivery-count").first())
      .toHaveTextContent("infra-critical доходит 2 из 2");
  });
});
