// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Alert Groups" (docs/concepts/alert-groups.md) and "Statuses and commands"
// (docs/concepts/statuses-and-commands.md). As a clock spec it runs alone, last, on a muster dev of its own: it turns
// the demo Integration's Heartbeat off, so that moving the development clock loses nothing, and creates a Personal
// access token for its own API calls before the first move. The fake Alertmanager sends five alerts to the
// Integration "prod-eu", five minutes of the development clock apart, two of them to the Route "payments": HighErrorRate
// stays firing with a Note, KubePodCrashLooping is acknowledged in the web UI, DiskAlmostFull snoozed until tomorrow
// 09:00, CertificateExpiring resolved with a Note, and KubeJobFailed acknowledged and then resolved by Alertmanager, so
// that the Statistics have times to acknowledge and to resolve. After each move of the clock the Admin signs in again.
// The times the images show keep their distance from the business time at the start, so that the real time the steps
// take does not move them; the ends of the Snooze presets are shown for that fixed start, a Wednesday 09:40.

import { expect, request, test, type APIRequestContext, type Page } from "@playwright/test";

import {
  adminApi,
  advance,
  APP,
  createRoute,
  devClockOffset,
  fakeIntegration,
  fam,
  notify,
  signInAdmin,
} from "../support";
import { capture, cardOf, nav, scrollToTop, type CaptureOptions } from "./capture";

const GROUPS = "concepts/alert-groups";
const COMMANDS = "concepts/statuses-and-commands";
const INTEGRATION = "prod-eu";
/** The alerts, in the order the fake Alertmanager sends them. */
const ALERTS = {
  error: {
    labels: {
      alertname: "HighErrorRate",
      service: "checkout",
      severity: "critical",
      cluster: "prod",
    },
    annotations: { summary: "Error rate of checkout is above 5 %" },
  },
  crash: {
    labels: {
      alertname: "KubePodCrashLooping",
      team: "payments",
      namespace: "payments",
      pod: "api-1",
      severity: "warning",
      cluster: "prod",
    },
    annotations: { summary: "Pod payments/api-1 is restarting" },
  },
  disk: {
    labels: { alertname: "DiskAlmostFull", instance: "db-1", severity: "warning", cluster: "prod" },
    annotations: { summary: "Disk of db-1 is 91 % full" },
  },
  cert: {
    labels: {
      alertname: "CertificateExpiring",
      domain: "shop.example.org",
      severity: "info",
      cluster: "prod",
    },
    annotations: { summary: "The certificate of shop.example.org expires in 14 days" },
  },
  job: {
    labels: {
      alertname: "KubeJobFailed",
      team: "payments",
      job_name: "invoice-export",
      severity: "warning",
      cluster: "prod",
    },
    annotations: { summary: "Job payments/invoice-export failed" },
  },
} as const;
type Name = keyof typeof ALERTS;

test.describe.configure({ mode: "serial" });

let integrationId = "";
/** The API with the Personal access token, which outlives the sessions that a move of the clock may end. */
let api: APIRequestContext | undefined;
/** The business time when the spec started: the times of the images keep their distance from it. */
let reference = 0;

/** Reads path with the Personal access token. */
async function get<T>(path: string): Promise<T> {
  const res = await api?.get(path);
  expect(res?.ok(), `GET ${path}`).toBe(true);
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the resource as the API reads it
  return (await res?.json()) as T;
}

/** The Alert Group of the alert name, as the Integration's Alerts view names it. */
async function groupOf(name: Name): Promise<{ id: string; number: number }> {
  const label = encodeURIComponent(`alertname="${ALERTS[name].labels.alertname}"`);
  const body = await get<{ items: { alert_group?: { id: string; number: number } }[] }>(
    `/api/v1/integrations/${integrationId}/alerts?label=${label}`,
  );
  const group = body.items[0]?.alert_group;
  expect(group, `the Alert Group of ${name}`).toBeDefined();
  return group ?? { id: "", number: 0 };
}

/** Sends the alert name, firing or resolved, and waits until Muster processed it. */
async function send(name: Name, status: "firing" | "resolved" = "firing"): Promise<void> {
  const alert = ALERTS[name];
  await fam("PUT", `/groups/${name}`, {
    receiver: INTEGRATION,
    route: "{}",
    labels: { alertname: alert.labels.alertname },
  });
  await fam("PUT", `/groups/${name}/alerts/a`, { ...alert, status });
  await notify(integrationId, name, {
    reason: status === "firing" ? "first notification" : "some alerts resolved",
  });
}

/** Moves the development clock and signs the Admin in again, as a move can end the session. */
async function move(page: Page, minutes: number): Promise<void> {
  await advance(minutes * 60);
  await page.context().clearCookies();
  await signInAdmin(page);
}

/** Captures with the times kept at their distance from the start of the spec. */
async function shoot(
  page: Page,
  docsPage: string,
  name: string,
  options: CaptureOptions = {},
): Promise<void> {
  await capture(page, docsPage, name, { reference, ...options });
}

test.beforeAll(async () => {
  const admin = await adminApi();
  try {
    // The demo Heartbeat off, so that no move of the clock makes it lost.
    const demo = await admin.call<{ items: { id: string; name: string }[] }>(
      "GET",
      "/api/v1/integrations?limit=500",
    );
    for (const i of demo.items.filter((x) => x.name === "dev-alertmanager")) {
      const current = await admin.call<{
        name: string;
        description?: string;
        connection_mode: string;
        static_labels: Record<string, string>;
        duplicate_window_seconds: number;
        etag: string;
      }>("GET", `/api/v1/integrations/${i.id}`);
      await admin.call(
        "PUT",
        `/api/v1/integrations/${i.id}`,
        {
          name: current.name,
          description: current.description ?? "",
          connection_mode: current.connection_mode,
          static_labels: current.static_labels,
          duplicate_window_seconds: current.duplicate_window_seconds,
          heartbeat: { enabled: false },
        },
        { "If-Match": current.etag },
      );
    }
    // A Personal access token for the spec's own API calls, made before the first move of the clock.
    const me = await admin.call<{ permissions: string[] }>("GET", "/api/v1/me");
    const token = await admin.call<{ value: string }>("POST", "/api/v1/me/personal-access-tokens", {
      name: "docs-screenshots",
      permissions: me.permissions,
    });
    api = await request.newContext({
      baseURL: APP,
      extraHTTPHeaders: { Authorization: `Bearer ${token.value}` },
    });
  } finally {
    await admin.dispose();
  }
  integrationId = await fakeIntegration(INTEGRATION);
  await createRoute("payments", "payments", ["alertname", "severity", "cluster"]);
  reference = Date.now() + (await devClockOffset()) * 1000;
});

test.afterAll(async () => {
  await api?.dispose();
});

test("Alert Groups in each status, their commands, the list and the Statistics", async ({
  page,
}) => {
  test.setTimeout(300_000);
  await signInAdmin(page);

  // Five alerts, five minutes apart.
  await send("error");
  for (const name of ["crash", "disk", "cert", "job"] as const) {
    await advance(300);
    await send(name);
  }
  await move(page, 10);

  // Commands in the web UI: a firing Alert Group, its command buttons and the Note box.
  const error = await groupOf("error");
  await page.goto(`/alert-groups/${error.id}`);
  const status = page.getByTestId("alert-group-status");
  await expect(status).toHaveText("Firing");
  const buttons = page.getByTestId("command-buttons");
  await expect(buttons.getByRole("button", { name: "Acknowledge", exact: true })).toBeVisible();
  await shoot(page, COMMANDS, "02-command-buttons", { waitFor: buttons });
  const note = page.getByTestId("note-box");
  await note
    .getByRole("textbox")
    .fill("Rollback of checkout 2.14 started; watching the error rate.");
  await scrollToTop(note);
  await shoot(page, COMMANDS, "03-note-box", {
    locator: note,
    padding: 16,
    highlight: note.getByRole("button", { name: "Add note" }),
  });
  await note.getByRole("button", { name: "Add note" }).click();
  await expect(
    page.getByTestId("note-body").getByText("Rollback of checkout 2.14 started", { exact: false }),
  ).toBeVisible();

  // KubeJobFailed is acknowledged 10 minutes after it started, KubePodCrashLooping 30 minutes after, in the web UI: the
  // Owner and the commands of an acknowledged Alert Group. Whole minutes keep the medians of the Statistics whole.
  const job = await groupOf("job");
  const acked = await api?.post(`/api/v1/alert-groups/${job.id}/acknowledge`);
  expect(acked?.ok(), "acknowledge KubeJobFailed").toBe(true);
  await move(page, 5);
  const crash = await groupOf("crash");
  await page.goto(`/alert-groups/${crash.id}`);
  await page.getByRole("button", { name: "Acknowledge", exact: true }).click();
  await expect(status).toHaveText("Acknowledged");
  await expect(page.getByTestId("alert-group-owner")).toHaveText("Owner: admin");
  await shoot(page, COMMANDS, "06-acknowledged", {
    waitFor: page.getByTestId("alert-group-owner"),
  });

  // Resolve with a Note.
  const cert = await groupOf("cert");
  await page.goto(`/alert-groups/${cert.id}`);
  await page.getByRole("button", { name: "Resolve", exact: true }).click();
  const resolve = page.getByRole("dialog", { name: `Resolve #${cert.number}` });
  await resolve.getByLabel("Add a note (optional)").fill("Renewed by cert-manager.");
  await shoot(page, COMMANDS, "05-resolve-dialog", {
    locator: resolve,
    highlight: resolve.getByRole("button", { name: "Resolve", exact: true }),
  });
  await resolve.getByRole("button", { name: "Resolve", exact: true }).click();
  await expect(status).toHaveText("Resolved");
  await move(page, 5);

  // KubeJobFailed resolves in Alertmanager.
  await send("job", "resolved");

  // Snooze: the presets, then the dialog with No end, then until tomorrow 09:00.
  const disk = await groupOf("disk");
  await page.goto(`/alert-groups/${disk.id}`);
  await page.getByRole("button", { name: "Snooze", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: `Snooze #${disk.number}` });
  const tomorrow = dialog.getByTestId("snooze-preset-end-tomorrow");
  const weekStart = dialog.getByTestId("snooze-preset-end-week_start");
  await expect(tomorrow).toHaveText(/^Ends .+, 09:00$/);
  const ends: [string, string][] = [
    [(await tomorrow.textContent()) ?? "", "Ends Oct 15, 2026, 09:00"],
    [(await weekStart.textContent()) ?? "", "Ends Oct 19, 2026, 09:00"],
  ];
  await dialog.getByTestId("snooze-choice-tomorrow").click();
  await shoot(page, COMMANDS, "01-snooze-presets", { locator: dialog, replace: ends });
  await dialog.getByText("No end", { exact: true }).click();
  await expect(dialog.getByTestId("snooze-no-end-warning")).toBeVisible();
  await shoot(page, COMMANDS, "04-snooze-dialog", { locator: dialog, replace: ends });
  await dialog.getByTestId("snooze-choice-tomorrow").click();
  await dialog.getByRole("button", { name: "Snooze", exact: true }).click();
  await expect(status).toHaveText("Snoozed");
  const until = (await page.getByTestId("alert-group-snooze").textContent()) ?? "";

  // In the web UI: the list of every status, its filters, an Alert Group page and its Timeline.
  await nav(page, "Alert Groups").click();
  await page.getByRole("tab", { name: /^All \d+$/ }).click();
  const table = page.getByRole("table", { name: "Alert Groups" });
  await expect(table.getByTestId("alert-group-status")).toHaveCount(5);
  const snoozedUntil: [string, string][] = [[until, "Snoozed until Oct 15, 2026, 09:00"]];
  await shoot(page, GROUPS, "01-list", { waitFor: table, replace: snoozedUntil });
  // The table scrolls sideways inside its box: from the Owner on, the end of a Snooze, the start and the duration. The
  // Snooze ends at tomorrow 09:00, a time that does not follow now: it shows for the fixed now of the images.
  const box = table.locator("xpath=..");
  await box.evaluate((el) => {
    const owner = [...el.querySelectorAll("th")].find((th) => th.textContent?.trim() === "Owner");
    el.scrollLeft = (owner instanceof HTMLElement ? owner.offsetLeft : el.scrollWidth) - 1;
  });
  const snoozeEnd = table.getByTestId("snooze-end").filter({ hasText: /\d/ });
  await expect(snoozeEnd).toHaveCount(1);
  const columns: [string, string][] = [
    [(await snoozeEnd.textContent()) ?? "", "Oct 15, 2026, 09:00"],
  ];
  await shoot(page, GROUPS, "02-list-columns", { locator: box, replace: columns });
  await box.evaluate((el) => {
    el.scrollLeft = 0;
  });
  await page.getByTestId("filter-route").getByRole("combobox").selectOption({ label: "payments" });
  await expect(table.getByTestId("alert-group-status")).toHaveCount(2);
  await shoot(page, GROUPS, "03-filters", { waitFor: table, replace: snoozedUntil });

  await page.goto(`/alert-groups/${crash.id}`);
  await expect(status).toHaveText("Acknowledged");
  await shoot(page, GROUPS, "04-alert-group", { waitFor: page.getByTestId("alert-group-owner") });
  const timeline = cardOf(page.getByRole("heading", { name: "Timeline", exact: true }));
  await scrollToTop(timeline);
  await shoot(page, GROUPS, "05-timeline", { locator: timeline });

  // Statistics: by route, and the days of one route.
  await nav(page, "Statistics").click();
  await expect(page.getByRole("radio", { name: "By route" })).toBeChecked();
  const payments = page
    .getByTestId("statistics-row")
    .filter({ has: page.getByTestId("statistics-name").getByText("payments", { exact: true }) });
  await expect(payments.getByTestId("statistics-count")).toHaveText("2");
  await shoot(page, GROUPS, "06-statistics", { waitFor: payments });
  await payments.getByRole("button", { name: "payments" }).click();
  const days = page.getByRole("table", { name: "Alert Groups per day of start: payments" });
  await expect(days.getByTestId("day-count")).toHaveText("2");
  await scrollToTop(days);
  await shoot(page, GROUPS, "07-statistics-days", { locator: days, padding: 16 });
});
