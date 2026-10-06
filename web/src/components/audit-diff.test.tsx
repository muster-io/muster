// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The names of the changed fields in the Audit log: every field that the server records for a resource type has one in
// English and Russian, and a field without one shows its JSON pointer.

import { afterEach, describe, expect, test } from "vitest";

import i18n from "../i18n";
import { fieldLabel } from "./audit-diff";

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

  test("shows the pointer of a field without a name", () => {
    expect(fieldLabel(i18n.t, "service_account", "/unknown")).toBe("/unknown");
    expect(fieldLabel(i18n.t, "api_token", "/name")).toBe("/name");
    expect(fieldLabel(i18n.t, null, "/role")).toBe("/role");
  });
});
