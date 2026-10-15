// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The System status sections in a real browser: every section of a full status in its order, each entity a link to
// its page and no other control, "None" for the empty sections, the Leader badge and the key ids, the recovery and the
// Integration texts, the outgoing heartbeat results — a failure translated by its class, with the server's text as its
// detail — and its suggestion, the Russian plurals, and the reads again on the four hints and every 30 seconds.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { render } from "vitest-browser-react";

import { getGetSystemStatusQueryKey } from "../api/gen/endpoints/system/system";
import type {
  HintEventType,
  OutgoingHeartbeatResult,
  Session,
  SystemStatus,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { applyHint } from "../lib/live";
import { browserTimeZone, formatDateTime } from "../lib/time";
import {
  SYSTEM_STATUS_REFRESH_MS,
  SystemStatusSections,
  heartbeatFailureText,
  heartbeatSentText,
  useSystemStatus,
} from "./system-status-sections";

const T1 = "2026-10-13T08:00:00Z";
const T2 = "2026-10-13T08:30:00Z";
const T3 = "2026-10-13T09:30:00Z";
const fmt = (iso: string) => formatDateTime(iso, browserTimeZone(), "en");

const FULL: SystemStatus = {
  version: "1.2.3",
  commit: "abc1234",
  replicas: [
    { id: "replica-a", leader: true, key_ids: ["k1", "k2"], refreshed_at: T3 },
    { id: "replica-b", leader: false, key_ids: [], refreshed_at: T3 },
  ],
  recovery: { active: true, period_from: T1, period_to: T2, until: T3 },
  delivery_queues: [{ destination: { id: "DS0000000000AA", name: "queued" }, queued: 3 }],
  broken_destinations: [
    { destination: { id: "DS0000000000AB", name: "alerts" }, since: T1, reason: "answered 403" },
  ],
  integrations_attention: [
    { integration: { id: "NT0000000000AA", name: "hb" }, issue: "heartbeat_lost", since: T2 },
    { integration: { id: "NT0000000000AB", name: "lab" }, issue: "heartbeat_lost", since: null },
    { integration: { id: "NT0000000000AC", name: "am" }, issue: "snapshot_truncated" },
  ],
  template_errors: [
    {
      subject_kind: "route",
      subject: { id: "RT0000000000AA", name: "db" },
      template: "root_message",
      since: T1,
      error: "line 1: boom",
    },
    {
      subject_kind: "destination",
      subject: { id: "DS0000000000AC", name: "hook" },
      template: "webhook_request",
      since: T2,
      error: "bad body",
    },
  ],
  active_storms: [
    { route: { id: "RT0000000000AB", name: "noisy" }, since: T2, alert_group_count: 21 },
  ],
  outgoing_heartbeat: { configured: true, last_attempt_at: T3, last_ok: true, last_error: null },
};

const EMPTY: SystemStatus = {
  version: "1.2.3",
  replicas: [],
  recovery: { active: false, period_from: T1, period_to: T2, until: null },
  delivery_queues: [],
  broken_destinations: [],
  integrations_attention: [],
  template_errors: [],
  active_storms: [],
  outgoing_heartbeat: { configured: false },
};

const SUGGESTION = "Set up an outgoing heartbeat so that a stopped Muster is noticed.";
const CONTROLS =
  "button,input,select,textarea,[role=button],[role=switch],[role=checkbox],[role=combobox]";

function newClient(): QueryClient {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the sections read only the time zone
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-13T19:00:00Z",
      idle_expires_at: "2026-10-13T19:00:00Z",
      method: "local",
      permissions: ["system-status:read", "organization:write"],
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return queryClient;
}

async function renderIn(queryClient: QueryClient, children: () => ReactNode) {
  const rootRoute = createRootRoute({ component: children });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

async function renderSections(status: SystemStatus, canSetUpHeartbeat = true) {
  const screen = await renderIn(newClient(), () => (
    <SystemStatusSections status={status} canSetUpHeartbeat={canSetUpHeartbeat} />
  ));
  await expect.element(screen.getByTestId("system-status-version")).toBeVisible();
  return screen;
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the System status sections", () => {
  test("show every section in the order of the status, with the version and its commit", async () => {
    const screen = await renderSections(FULL);
    await expect
      .element(screen.getByTestId("system-status-version"))
      .toHaveTextContent("Muster 1.2.3 (abc1234)");
    const headings = [...screen.container.querySelectorAll("h2")].map((h) => h.textContent);
    expect(headings).toEqual([
      "Replicas",
      "Recovery after downtime",
      "Delivery queues",
      "Broken Destinations",
      "Integrations needing attention",
      "Template errors",
      "Storms",
      "Outgoing heartbeat",
    ]);
    await expect
      .element(screen.getByRole("region", { name: "Broken Destinations" }))
      .toMatchTextContent(`alerts · since ${fmt(T1)}answered 403`);
    await expect
      .element(screen.getByRole("region", { name: "Delivery queues" }))
      .toMatchTextContent("queued · 3 deliveries queued");
    await expect
      .element(screen.getByRole("region", { name: "Storms" }))
      .toMatchTextContent(`noisy · since ${fmt(T2)} · 21 new Alert Groups`);
    await expect
      .element(screen.getByRole("region", { name: "Template errors" }))
      .toMatchTextContent(
        `db · Route · Root message · since ${fmt(T1)}line 1: boom` +
          `hook · Destination · Request template · since ${fmt(T2)}bad body`,
      );
  });

  test("link each entity to its page, a template error by its subject", async () => {
    const screen = await renderSections(FULL);
    const href = (region: string, name: string) =>
      screen
        .getByRole("region", { name: region })
        .getByRole("link", { name, exact: true })
        .element()
        .getAttribute("href");
    expect(href("Delivery queues", "queued")).toBe("/destinations/DS0000000000AA");
    expect(href("Broken Destinations", "alerts")).toBe("/destinations/DS0000000000AB");
    expect(href("Integrations needing attention", "hb")).toBe("/integrations/NT0000000000AA");
    expect(href("Template errors", "db")).toBe("/routes/RT0000000000AA");
    expect(href("Template errors", "hook")).toBe("/destinations/DS0000000000AC");
    expect(href("Storms", "noisy")).toBe("/routes/RT0000000000AB");
  });

  test("have no control other than links", async () => {
    const full = await renderSections(FULL);
    expect(full.container.querySelectorAll(CONTROLS)).toHaveLength(0);
    expect(full.container.querySelectorAll("a").length).toBeGreaterThan(0);
    await full.unmount();
    const empty = await renderSections(EMPTY);
    expect(empty.container.querySelectorAll(CONTROLS)).toHaveLength(0);
  });

  test("say None for the empty sections and suggest the outgoing heartbeat", async () => {
    const screen = await renderSections(EMPTY);
    await expect
      .element(screen.getByTestId("system-status-version"))
      .toHaveTextContent("Muster 1.2.3");
    expect(screen.getByText("None", { exact: true }).elements()).toHaveLength(6);
    await expect
      .element(screen.getByTestId("system-status-recovery"))
      .toHaveTextContent("Not recovering");
    const link = screen.getByRole("link", { name: SUGGESTION });
    await expect.element(link).toHaveAttribute("href", "/admin/organization/outgoing-heartbeat");
    await screen.unmount();

    const reader = await renderSections(EMPTY, false);
    await expect.element(reader.getByText(SUGGESTION)).toBeVisible();
    expect(reader.container.querySelectorAll("a")).toHaveLength(0);
  });

  test("mark only the Leader and list the key ids of each replica", async () => {
    const screen = await renderSections(FULL);
    expect(screen.getByTestId("replica-leader").elements()).toHaveLength(1);
    const rows = screen.getByTestId("replica");
    await expect
      .element(rows.nth(0))
      .toHaveTextContent(`replica-aLeader · Keys: k1, k2 · Refreshed ${fmt(T3)}`);
    await expect
      .element(rows.nth(1))
      .toHaveTextContent(`replica-b · No keys · Refreshed ${fmt(T3)}`);
    await expect.element(rows.nth(1).getByTestId("replica-leader")).not.toBeInTheDocument();
  });

  test("show the recovery and the Integrations that need attention", async () => {
    const screen = await renderSections(FULL);
    await expect
      .element(screen.getByTestId("system-status-recovery"))
      .toHaveTextContent(
        `Recovering until ${fmt(T3)} after downtime from ${fmt(T1)} to ${fmt(T2)}`,
      );
    const items = screen.getByRole("region", { name: "Integrations needing attention" });
    await expect
      .element(items.getByRole("listitem").nth(0))
      .toHaveTextContent(`hb — Heartbeat lost since ${fmt(T2)}`);
    await expect
      .element(items.getByRole("listitem").nth(1))
      .toHaveTextContent("lab — Heartbeat lost");
    await expect
      .element(items.getByRole("listitem").nth(2))
      .toHaveTextContent("am — Snapshots truncated");
    await screen.unmount();

    const noPeriod = await renderSections({
      ...EMPTY,
      recovery: { active: true, until: T3 },
    });
    await expect
      .element(noPeriod.getByTestId("system-status-recovery"))
      .toHaveTextContent(`Recovering until ${fmt(T3)}`);
  });

  test.each<[string, OutgoingHeartbeatResult, string]>([
    ["OK", { configured: true, last_attempt_at: T3, last_ok: true }, `Last sent ${fmt(T3)}: OK`],
    [
      "failed with an answer",
      {
        configured: true,
        last_attempt_at: T3,
        last_ok: false,
        last_error: "answered 503 (transient)",
        last_error_class: "transient",
        last_status: 503,
      },
      `Last sent ${fmt(T3)}: a temporary error (answered 503)`,
    ],
    [
      "failed without an answer",
      {
        configured: true,
        last_attempt_at: T3,
        last_ok: false,
        last_error: "dns: lookup hc.example.org: no such host",
        last_error_class: "dns",
        last_status: null,
      },
      `Last sent ${fmt(T3)}: the host name does not resolve`,
    ],
    [
      "failed with an error without a class",
      { configured: true, last_attempt_at: T3, last_ok: false, last_error: "answered 503" },
      `Last sent ${fmt(T3)}: answered 503`,
    ],
    [
      "failed without an error",
      { configured: true, last_attempt_at: T3, last_ok: false, last_error: null },
      `Last sent ${fmt(T3)}: failed`,
    ],
    ["not sent yet", { configured: true, last_attempt_at: null }, "Not sent yet"],
  ])("show the last outgoing heartbeat: %s", async (_name, result, text) => {
    const screen = await renderSections({ ...EMPTY, outgoing_heartbeat: result });
    await expect.element(screen.getByTestId("system-status-heartbeat")).toHaveTextContent(text);
    await expect.element(screen.getByRole("link", { name: SUGGESTION })).not.toBeInTheDocument();
  });

  test("heartbeatSentText is null without a URL", () => {
    expect(heartbeatSentText(i18n.getFixedT("en"), { configured: false }, fmt)).toBeNull();
  });

  test("show the server's text of a classified failure as its detail, in Russian too", async () => {
    const failed: OutgoingHeartbeatResult = {
      configured: true,
      last_attempt_at: T3,
      last_ok: false,
      last_error: "answered 503 (transient)",
      last_error_class: "transient",
      last_status: 503,
    };
    const screen = await renderSections({ ...EMPTY, outgoing_heartbeat: failed });
    await expect
      .element(screen.getByTestId("system-status-heartbeat-detail"))
      .toHaveTextContent("Details: answered 503 (transient)");
    await screen.unmount();
    const ok = await renderSections({ ...EMPTY, outgoing_heartbeat: { ...failed, last_ok: true } });
    expect(ok.getByTestId("system-status-heartbeat-detail").query()).toBeNull();
    await ok.unmount();

    const ru = i18n.getFixedT("ru");
    expect(heartbeatSentText(ru, failed, fmt)).toBe(
      `Последняя отправка ${fmt(T3)}: временная ошибка (ответ 503)`,
    );
    for (const [errorClass, text] of [
      ["retry_after", "адрес попросил повторить позже"],
      ["fatal", "адрес отклонил запрос"],
      ["redirect", "перенаправление, по которому Muster не переходит"],
      ["unknown", "неожиданный ответ"],
      ["blocked", "заблокировано политикой исходящих адресов"],
      ["dns", "имя хоста не найдено"],
      ["connect", "не удалось подключиться"],
      ["tls", "ошибка TLS-соединения"],
      ["timeout", "нет ответа за отведённое время"],
      ["proxy_auth", "прокси отклонил учётные данные"],
      ["internal", "Muster не смог подготовить запрос"],
    ] as const) {
      expect(
        heartbeatFailureText(ru, { ...failed, last_error_class: errorClass, last_status: null }),
      ).toBe(text);
    }
  });
});

describe("translations", () => {
  test("the queued deliveries and the Storm counts in both languages", () => {
    const en = i18n.getFixedT("en");
    expect(en("systemStatus.queues.queued", { count: 1 })).toBe("1 delivery queued");
    expect(en("systemStatus.queues.queued", { count: 2 })).toBe("2 deliveries queued");
    expect(en("systemStatus.storms.count", { count: 1 })).toBe("1 new Alert Group");
    expect(en("systemStatus.storms.count", { count: 5 })).toBe("5 new Alert Groups");
    const ru = i18n.getFixedT("ru");
    expect(ru("systemStatus.queues.queued", { count: 1 })).toBe("1 доставка в очереди");
    expect(ru("systemStatus.queues.queued", { count: 3 })).toBe("3 доставки в очереди");
    expect(ru("systemStatus.queues.queued", { count: 5 })).toBe("5 доставок в очереди");
    expect(ru("systemStatus.queues.queued", { count: 21 })).toBe("21 доставка в очереди");
    expect(ru("systemStatus.storms.count", { count: 1 })).toBe("1 новая группа алертов");
    expect(ru("systemStatus.storms.count", { count: 3 })).toBe("3 новые группы алертов");
    expect(ru("systemStatus.storms.count", { count: 5 })).toBe("5 новых групп алертов");
    expect(ru("systemStatus.storms.count", { count: 21 })).toBe("21 новая группа алертов");
  });
});

/** Reads the System status as both pages do and shows its version. */
function Probe({ enabled }: { enabled: boolean }) {
  const status = useSystemStatus(enabled);
  return <p data-testid="probe">{status.data?.version ?? "none"}</p>;
}

describe("refresh", () => {
  let reads = 0;

  beforeEach(() => {
    reads = 0;
    vi.spyOn(window, "fetch").mockImplementation((input) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url.endsWith("/api/v1/system-status")) {
        reads++;
        return Promise.resolve(
          new Response(JSON.stringify(FULL), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Promise.resolve(new Response("{}", { status: 404 }));
    });
  });

  test("reads again every 30 seconds and on the four hints", async () => {
    const queryClient = newClient();
    const screen = await renderIn(queryClient, () => <Probe enabled />);
    await expect.element(screen.getByTestId("probe")).toHaveTextContent("1.2.3");
    expect(reads).toBe(1);
    const query = queryClient.getQueryCache().find({ queryKey: getGetSystemStatusQueryKey() });
    expect(query?.observers[0]?.options.refetchInterval).toBe(SYSTEM_STATUS_REFRESH_MS);
    expect(SYSTEM_STATUS_REFRESH_MS).toBe(30_000);

    const hints: [HintEventType, string | null][] = [
      ["system-status", null],
      ["destination", "DS0000000000AB"],
      ["integration", "NT0000000000AA"],
      ["organization", null],
    ];
    for (const [type, id] of hints) {
      const before = reads;
      applyHint(queryClient, { type, id });
      await vi.waitFor(() => expect(reads).toBe(before + 1));
    }
  });

  test("does not read when disabled", async () => {
    const screen = await renderIn(newClient(), () => <Probe enabled={false} />);
    await expect.element(screen.getByTestId("probe")).toHaveTextContent("none");
    expect(reads).toBe(0);
  });
});
