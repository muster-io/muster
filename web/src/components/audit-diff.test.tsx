// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The names of the changed fields in the Audit log: every field that the server records for a resource type has one in
// English and Russian, and a field without one shows its JSON pointer. A changed Secret shows only that it changed.

import { I18nextProvider } from "react-i18next";
import { afterEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { AuditDiff, fieldLabel } from "./audit-diff";

/** Every resource type with a diff and the pointers the server writes for it. */
const AUDITED: Record<string, readonly string[]> = {
  user: [
    "/name",
    "/login",
    "/email",
    "/role",
    "/status",
    "/password",
    "/sign_in_method",
    "/time_zone",
    "/language",
  ],
  service_account: ["/name", "/role", "/status"],
  integration: [
    "/name",
    "/description",
    "/connection_mode",
    "/static_labels",
    "/duplicate_window_seconds",
    "/heartbeat/enabled",
    "/heartbeat/timeout_seconds",
    "/deleted_at",
  ],
  oidc_settings: [
    "/enabled",
    "/display_name",
    "/issuer_url",
    "/client_id",
    "/client_secret",
    "/client_secret_expires_on",
    "/scopes",
    "/groups_claim",
    "/group_mappings",
    "/unmatched_role",
    "/sync_role",
    "/skip_totp_with_idp_mfa",
    "/proxy/enabled",
    "/proxy/type",
    "/proxy/address",
    "/proxy/username",
    "/proxy/password",
  ],
  organization: ["/totp_required"],
  route: [
    "/name",
    "/description",
    "/matchers",
    "/urgent",
    "/group_key",
    "/destination_ids",
    "/policy/reopen_window_seconds",
    "/policy/grace_period_seconds",
    "/policy/urgent_rise_removes_ack",
    "/policy/snooze_durations_seconds",
    "/policy/thread_batching_window_seconds",
    "/policy/storm_threshold",
    "/policy/language",
    "/policy/templates/root_message",
    "/policy/templates/line",
    "/policy/templates/ack_timeout_notice",
    "/policy/ack_timeout/enabled",
    "/policy/ack_timeout/first_interval_seconds",
    "/policy/reminders/enabled",
    "/policy/reminders/first_interval_seconds",
    "/policy/reminders/cap_seconds",
    "/policy/auto_unacknowledge",
    "/deleted_at",
    "/route_ids",
  ],
  lookup_table: ["/name", "/description", "/columns", "/entries"],
  link_rule: ["/name", "/matchers", "/scope/type", "/scope/label", "/url_template"],
  connection: [
    "/name",
    "/server_url",
    "/bot_token",
    "/limiter/limit",
    "/limiter/per_seconds",
    "/proxy/enabled",
    "/proxy/type",
    "/proxy/address",
    "/proxy/username",
    "/proxy/password",
    "/deleted_at",
  ],
  destination: [
    "/name",
    "/connection_id",
    "/team_id",
    "/channel_id",
    "/mentions",
    "/limiter/limit",
    "/limiter/per_seconds",
    "/deleted_at",
  ],
};

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("fieldLabel", () => {
  test.each(["en", "ru"])("names every audited field in %s", async (language) => {
    await i18n.changeLanguage(language);
    for (const [resourceType, pointers] of Object.entries(AUDITED)) {
      for (const pointer of pointers) {
        const label = fieldLabel(i18n.t, resourceType, pointer);
        expect(label, `${resourceType} ${pointer}`).not.toBe(pointer);
        expect(label, `${resourceType} ${pointer}`).not.toMatch(/^[a-z]+(\.[a-zA-Z]+)+$/);
      }
    }
  });

  test("names the Role of a Service account", async () => {
    expect(fieldLabel(i18n.t, "service_account", "/role")).toBe("Role");
    await i18n.changeLanguage("ru");
    expect(fieldLabel(i18n.t, "service_account", "/role")).toBe("Роль");
  });

  test("names the rows of a Lookup table and the URL template of a Link rule", async () => {
    expect(fieldLabel(i18n.t, "lookup_table", "/entries")).toBe("Rows");
    expect(fieldLabel(i18n.t, "link_rule", "/url_template")).toBe("URL template");
    await i18n.changeLanguage("ru");
    expect(fieldLabel(i18n.t, "lookup_table", "/entries")).toBe("Строки");
    expect(fieldLabel(i18n.t, "link_rule", "/url_template")).toBe("Шаблон URL");
  });

  test("names the server URL, the bot token and the limiter of a Connection", async () => {
    expect(fieldLabel(i18n.t, "connection", "/server_url")).toBe("Server URL");
    expect(fieldLabel(i18n.t, "connection", "/bot_token")).toBe("Bot token");
    expect(fieldLabel(i18n.t, "connection", "/limiter/limit")).toBe("Rate limit: requests");
    await i18n.changeLanguage("ru");
    expect(fieldLabel(i18n.t, "connection", "/server_url")).toBe("URL сервера");
    expect(fieldLabel(i18n.t, "connection", "/bot_token")).toBe("Токен бота");
  });

  test("names the Destinations and the delivery fields of a Route", async () => {
    expect(fieldLabel(i18n.t, "route", "/destination_ids")).toBe("Destinations");
    expect(fieldLabel(i18n.t, "route", "/policy/thread_batching_window_seconds")).toBe(
      "Thread batching window (seconds)",
    );
    expect(fieldLabel(i18n.t, "route", "/policy/storm_threshold")).toBe(
      "Storm threshold (new Alert Groups per minute)",
    );
    await i18n.changeLanguage("ru");
    expect(fieldLabel(i18n.t, "route", "/destination_ids")).toBe("Места доставки");
  });

  test("names the Connection, team, channel, Mentions and limiter of a Destination", async () => {
    expect(fieldLabel(i18n.t, "destination", "/connection_id")).toBe("Connection");
    expect(fieldLabel(i18n.t, "destination", "/team_id")).toBe("Team");
    expect(fieldLabel(i18n.t, "destination", "/channel_id")).toBe("Channel");
    expect(fieldLabel(i18n.t, "destination", "/mentions")).toBe("Mentions");
    expect(fieldLabel(i18n.t, "destination", "/limiter/per_seconds")).toBe(
      "Rate limit: period (seconds)",
    );
    await i18n.changeLanguage("ru");
    expect(fieldLabel(i18n.t, "destination", "/channel_id")).toBe("Канал");
    expect(fieldLabel(i18n.t, "destination", "/mentions")).toBe("Упоминания");
  });

  test("shows the pointer of a field without a name", () => {
    expect(fieldLabel(i18n.t, "service_account", "/unknown")).toBe("/unknown");
    expect(fieldLabel(i18n.t, "api_token", "/name")).toBe("/name");
    expect(fieldLabel(i18n.t, null, "/role")).toBe("/role");
  });
});

describe("AuditDiff", () => {
  test("shows a replaced bot token of a Connection as a changed Secret, never its value", async () => {
    const screen = await render(
      <I18nextProvider i18n={i18n}>
        <AuditDiff
          resourceType="connection"
          diff={[
            {
              pointer: "/server_url",
              before: "http://old.example.org",
              after: "http://127.0.0.1:18065",
            },
            { pointer: "/bot_token", secret_changed: true },
          ]}
        />
      </I18nextProvider>,
    );
    const diff = screen.getByTestId("audit-diff");
    await expect.element(diff).toBeVisible();
    const text = diff.element().textContent;
    expect(text).toContain("Bot token:changed");
    expect(text).toContain("Server URL:http://old.example.org");
    expect(text).not.toContain("mm-dev-token");
  });
});
