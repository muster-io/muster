// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The screenshots of "Connect Alertmanager", "Connect Grafana" and "Heartbeat" (docs/integrations/): the Admin creates
// the Integration "prod-eu" with the Static label cluster=prod-eu and its token, whose snippet the fake Alertmanager
// then sends with, and its page shows the first Snapshot; the Integration "grafana-prod" and its token, to which a
// Snapshot in Grafana's format goes by an HTTP request to the ingest endpoint, as Grafana's webhook contact point sends
// it; then the Heartbeat of "prod-eu" turned on, waiting, live from a signal of the fake Alertmanager's Heartbeat sender,
// and lost. The development clock stays where it is: the last signal is moved back in the database instead, and the
// Leader's Heartbeat check, every 10 seconds, finds it overdue. It deletes both Integrations, which resolves their Alert
// Groups and the Internal alert MusterHeartbeatLost.

import { expect, test, type Locator, type Page } from "@playwright/test";

import {
  adminApi,
  devClockOffset,
  fam,
  INGEST_URL,
  notify,
  processed,
  removeIntegration,
  signInAdmin,
  sql,
} from "../support";
import { capture, cardOf, nav, scrollToTop } from "./capture";

const ALERTMANAGER = "integrations/alertmanager";
const GRAFANA = "integrations/grafana";
const HEARTBEAT = "integrations/heartbeat";
const INTEGRATION = "prod-eu";
const GRAFANA_INTEGRATION = "grafana-prod";
/** The receiver, the group and the Heartbeat sender of the fake Alertmanager. */
const RECEIVER = "muster-prod-eu";
const AM_GROUP = "integrations-guide";
const SENDER = "muster-heartbeat-prod-eu";
const HEARTBEAT_URL = "http://localhost:8081/api/v1/heartbeat";
const ALERT = {
  labels: { alertname: "HighLatency", service: "checkout", severity: "warning" },
  annotations: { summary: "p99 latency of checkout is above 2 s" },
};

test.describe.configure({ mode: "serial" });

async function cleanUp(): Promise<void> {
  await fetch(`http://127.0.0.1:19093/_fake/heartbeats/${SENDER}`, { method: "DELETE" });
  const admin = await adminApi();
  try {
    await removeIntegration(admin, INTEGRATION);
    await removeIntegration(admin, GRAFANA_INTEGRATION);
  } finally {
    await admin.dispose();
  }
}

/** Fills the form of a new Integration: its name and the Static label cluster=prod-eu. */
async function fillIntegration(page: Page, name: string): Promise<void> {
  await nav(page, "Integrations").click();
  await page.getByRole("link", { name: "Create integration" }).click();
  await page.getByLabel("Name", { exact: true }).fill(name);
  await page.getByRole("button", { name: "Add label" }).click();
  await page.getByLabel("Label name").fill("cluster");
  await page.getByLabel("Label value").fill("prod-eu");
}

/** Creates a token named name in the dialog of the Integration page; returns the dialog and the token. */
async function createToken(page: Page, name: string): Promise<{ dialog: Locator; token: string }> {
  await page.getByRole("button", { name: "Create token" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill(name);
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog.getByText("You will not see this token again.")).toBeVisible();
  const token = (await dialog.getByTestId("integration-token-value").textContent()) ?? "";
  expect(token).toMatch(/^mstr_int_\S+$/);
  // The page behind, which shows at the dialog's corners, lists the new token before the shot.
  await expect(
    page.getByTestId("integration-token-name").getByText(name, { exact: true }),
  ).toBeVisible();
  return { dialog, token };
}

/** The id of the Integration whose page is open. */
function integrationIdOf(page: Page): string {
  return new URL(page.url()).pathname.split("/")[2] ?? "";
}

test.beforeAll(cleanUp);
test.afterAll(cleanUp);

test("connects Alertmanager and Grafana and turns the Heartbeat on", async ({ page }) => {
  test.setTimeout(240_000);
  await signInAdmin(page);
  const badge = page.getByTestId("heartbeat-badge");
  const banner = page.getByTestId("heartbeat-banner");

  // Connect Alertmanager: the form with the Static label, the token with the snippet, the first Snapshot.
  await fillIntegration(page, INTEGRATION);
  await capture(page, ALERTMANAGER, "01-new-integration", {
    highlight: page.getByRole("button", { name: "Create", exact: true }),
  });
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: INTEGRATION, level: 1 })).toBeVisible();
  const integrationId = integrationIdOf(page);
  await expect(badge).toHaveText("Heartbeat: Not configured");
  const { dialog, token } = await createToken(page, "alertmanager");
  await expect(dialog.getByTestId("integration-token-snippet")).toContainText(RECEIVER);
  await capture(page, ALERTMANAGER, "02-token-and-snippet", { locator: dialog });
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await fam("POST", "/receivers", { name: RECEIVER, url: INGEST_URL, token });
  await fam("PUT", `/groups/${AM_GROUP}`, {
    receiver: RECEIVER,
    route: "{}",
    labels: { alertname: ALERT.labels.alertname },
  });
  await fam("PUT", `/groups/${AM_GROUP}/alerts/a1`, ALERT);
  await notify(integrationId, AM_GROUP, { reason: "first notification" });
  await page.reload();
  await expect(page.getByTestId("integration-snapshot-count")).toHaveText("Snapshots received: 1");
  await expect(page.getByTestId("snapshot-state").first()).toHaveText("Processed");
  await capture(page, ALERTMANAGER, "03-first-snapshot", {
    waitFor: page.getByTestId("integration-last-snapshot"),
  });

  const stored = cardOf(page.getByRole("heading", { name: "Stored Snapshots", exact: true }));
  await scrollToTop(stored);
  await capture(page, ALERTMANAGER, "04-stored-snapshots", { locator: stored });

  // Static labels: the field of the Integration's settings.
  await page.getByRole("link", { name: "Edit" }).click();
  const labels = page.getByRole("group", { name: "Static labels" });
  await expect(labels.getByLabel("Label value")).toHaveValue("prod-eu");
  await capture(page, ALERTMANAGER, "05-static-labels", { locator: labels, padding: 16 });

  // Heartbeat: not configured, then turned on in the settings, waiting, the token with its configuration, live.
  await page.goto(`/integrations/${integrationId}`);
  await expect(badge).toHaveText("Heartbeat: Not configured");
  await capture(page, HEARTBEAT, "01-not-configured", { waitFor: banner });
  await page.getByRole("link", { name: "Edit" }).click();
  await page.getByRole("switch", { name: "Watch for Heartbeat signals" }).check();
  await expect(page.getByLabel("Timeout")).toHaveValue("5");
  await expect(page.getByLabel("Heartbeat URL")).toHaveValue(HEARTBEAT_URL);
  const heartbeat = page.getByRole("group", { name: "Heartbeat" });
  await scrollToTop(heartbeat);
  await capture(page, HEARTBEAT, "02-turn-on", { locator: heartbeat, padding: 16 });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(badge).toHaveText("Heartbeat: Waiting");
  await capture(page, HEARTBEAT, "03-waiting", { waitFor: banner });

  const second = await createToken(page, "heartbeat");
  await expect(second.dialog.getByTestId("integration-heartbeat-snippet")).toContainText(
    "/api/v1/heartbeat",
  );
  // The dialog scrolls to the Heartbeat configuration inside itself, leaving the page behind where it is.
  await second.dialog.evaluate((el) => {
    const box = [el, ...el.querySelectorAll("*")].find(
      (e) => e.scrollHeight > e.clientHeight + 1 && getComputedStyle(e).overflowY !== "visible",
    );
    if (box !== undefined) {
      box.scrollTop = box.scrollHeight;
    }
  });
  await capture(page, HEARTBEAT, "04-token-and-snippet", { locator: second.dialog });
  await second.dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await fam("POST", "/heartbeats", {
    name: SENDER,
    url: HEARTBEAT_URL,
    token: second.token,
    interval_seconds: 0,
  });
  await fam("POST", `/heartbeats/${SENDER}/send`);
  await expect(badge).toHaveText("Heartbeat: Live");
  await expect(banner).toHaveCount(0);
  await capture(page, HEARTBEAT, "05-live", { waitFor: badge });

  // When the Heartbeat is lost: the last signal ten minutes back, overdue for the timeout of 5 minutes.
  await sql(
    "UPDATE integrations SET heartbeat_last_signal_at = heartbeat_last_signal_at - interval '10 minutes' WHERE public_id = $1",
    [integrationId],
  );
  await expect(badge).toHaveText("Heartbeat: Lost", { timeout: 30_000 });
  await expect(banner).toContainText("No contact with Alertmanager since");
  await capture(page, HEARTBEAT, "06-lost", { waitFor: banner });

  // Connect Grafana: the form, the token, and a Snapshot as Grafana's webhook contact point sends it.
  await fillIntegration(page, GRAFANA_INTEGRATION);
  await capture(page, GRAFANA, "01-new-integration", {
    highlight: page.getByRole("button", { name: "Create", exact: true }),
  });
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: GRAFANA_INTEGRATION, level: 1 })).toBeVisible();
  const grafanaId = integrationIdOf(page);
  const grafana = await createToken(page, "grafana");
  await capture(page, GRAFANA, "02-token", {
    locator: grafana.dialog,
    highlight: grafana.dialog.getByTestId("integration-token-value"),
  });
  await grafana.dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  const startsAt = new Date(Date.now() + (await devClockOffset()) * 1000 - 60_000);
  startsAt.setMilliseconds(0);
  const sent = await fetch(INGEST_URL, {
    method: "POST",
    headers: { Authorization: `Bearer ${grafana.token}`, "Content-Type": "application/json" },
    body: JSON.stringify(grafanaSnapshot(startsAt.toISOString().replace(".000Z", "Z"))),
  });
  expect(sent.status, await sent.text()).toBe(202);
  await processed(grafanaId);
  await page.reload();
  await expect(page.getByTestId("integration-snapshot-count")).toHaveText("Snapshots received: 1");
  await expect(page.getByTestId("snapshot-state").first()).toHaveText("Processed");
  await capture(page, GRAFANA, "03-first-snapshot", {
    waitFor: page.getByTestId("integration-last-snapshot"),
  });

  const admin = await adminApi();
  try {
    const alerts = await admin.call<{ items: { labels: Record<string, string> }[] }>(
      "GET",
      `/api/v1/integrations/${grafanaId}/alerts`,
    );
    expect(alerts.items[0]?.labels).toMatchObject({
      alertname: "DiskAlmostFull",
      grafana_folder: "Storage",
      cluster: "prod-eu",
    });
  } finally {
    await admin.dispose();
  }
});

/** A Snapshot of one firing alert as Grafana's built-in Alertmanager sends it to a webhook contact point. */
function grafanaSnapshot(startsAt: string): Record<string, unknown> {
  const labels = {
    alertname: "DiskAlmostFull",
    grafana_folder: "Storage",
    instance: "db-1",
    severity: "warning",
  };
  const annotations = { summary: "Disk of db-1 is 91 % full" };
  return {
    receiver: "muster",
    status: "firing",
    orgId: 1,
    alerts: [
      {
        status: "firing",
        labels,
        annotations,
        startsAt,
        endsAt: "0001-01-01T00:00:00Z",
        generatorURL: "https://grafana.example.org/alerting/grafana/disk-almost-full/view?orgId=1",
        fingerprint: "7d3c1e0f9b2a4c58",
        silenceURL: "",
        dashboardURL: "",
        panelURL: "",
        values: { A: 91, B: 1 },
        valueString: "[ var='A' labels={instance=db-1} type='query' value=91 ]",
        orgId: 1,
      },
    ],
    groupLabels: { alertname: "DiskAlmostFull", grafana_folder: "Storage" },
    commonLabels: labels,
    commonAnnotations: annotations,
    externalURL: "https://grafana.example.org/",
    version: "1",
    groupKey: '{}/{}:{alertname="DiskAlmostFull", grafana_folder="Storage"}',
    truncatedAlerts: 0,
    title: "[FIRING:1] DiskAlmostFull Storage",
    state: "alerting",
    message: "",
  };
}
