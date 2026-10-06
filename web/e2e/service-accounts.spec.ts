// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Service accounts against `muster dev`: Verification step 5 of the API tokens pages story, with the Role change, a
// token's last use and revocation, the Audit log entry of an action made with a Service account token, and delete.

import { expect, test, type Page } from "@playwright/test";

import { APP, expectNoHorizontalScroll, shot, signInAdmin, watchCsp } from "./support";

const API_V4 = "http://127.0.0.1:8080/api/v1";

function accountRow(page: Page, name: string) {
  return page
    .getByRole("table", { name: "Service accounts" })
    .getByRole("row")
    .filter({ hasText: name });
}

function tokenRow(page: Page, name: string) {
  return page
    .getByRole("list", { name: "Tokens" })
    .getByTestId("token-row")
    .filter({ has: page.getByTestId("token-name").getByText(name, { exact: true }) });
}

test("creates a Service account with a token, changes its Role, disables, enables and deletes it", async ({
  page,
}) => {
  const csp = watchCsp(page);
  await signInAdmin(page);
  const nav = page.getByRole("navigation", { name: "Main" });
  await nav.getByRole("link", { name: "Service accounts" }).click();
  await expect(page).toHaveURL(`${APP}/admin/service-accounts`);
  await expect(page.getByText("No service accounts yet.")).toBeVisible();

  // 5. Create "terraform" with the Admin Role; its page opens.
  await page.getByRole("button", { name: "Create service account" }).click();
  const create = page.getByRole("dialog");
  await create.getByLabel("Name").fill("terraform");
  await create.getByLabel("Role").selectOption({ label: "Admin" });
  await shot(page, "service-accounts-create");
  await create.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("heading", { name: "terraform", level: 1 })).toBeVisible();
  await expect(page.getByTestId("sa-role")).toHaveText("Admin");
  await expect(page.getByTestId("sa-status")).toHaveText("Active");
  await expect(page.getByTestId("sa-tokens")).toHaveText("0");

  // A name is taken once.
  await page.getByRole("link", { name: "Service accounts" }).first().click();
  await page.getByRole("button", { name: "Create service account" }).click();
  await page.getByRole("dialog").getByLabel("Name").fill("Terraform");
  await page.getByRole("dialog").getByRole("button", { name: "Create", exact: true }).click();
  await expect(
    page.getByRole("dialog").getByText("Another service account has this name."),
  ).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
  await accountRow(page, "terraform").getByRole("link", { name: "terraform" }).click();

  // A token "ci": the value shows once and starts with mstr_sat_.
  await page.getByRole("button", { name: "Create token" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("ci");
  await expect(dialog.getByText("This token never expires.")).toBeVisible();
  await expect(dialog.getByRole("checkbox")).toHaveCount(0);
  await dialog.getByRole("button", { name: "Create", exact: true }).click();
  await expect(dialog.getByText("You will not see this token again.")).toBeVisible();
  const value = await dialog.getByTestId("token-value").inputValue();
  expect(value).toMatch(/^mstr_sat_\S+$/);
  await shot(page, "service-account-token-once");
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(tokenRow(page, "ci").getByText("This token never expires.")).toBeVisible();
  await expect(page.getByTestId("sa-tokens")).toHaveText("1");
  expect(await page.content()).not.toContain(value);

  // The token acts as the Service account; the Audit log names it and the token.
  const res = await fetch(`${API_V4}/users`, {
    method: "POST",
    headers: { Authorization: `Bearer ${value}`, "Content-Type": "application/json" },
    body: JSON.stringify({ name: "by-terraform", login: "by-terraform", role: "viewer" }),
  });
  expect(res.status).toBe(201);
  await page.reload();
  await expect(tokenRow(page, "ci").getByTestId("token-last-use")).toHaveText(
    /^Last used .+ from 127\.0\.0\.1$/,
  );
  await shot(page, "service-account-page");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "service-account-page-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  // Disable stops the token until Enable.
  await page.getByRole("button", { name: "Disable" }).click();
  await expect(page.getByTestId("sa-status")).toHaveText("Disabled");
  const list = await fetch(`${API_V4}/users`, { headers: { Authorization: `Bearer ${value}` } });
  expect(list.status).toBe(401);
  await page.getByRole("button", { name: "Enable" }).click();
  await expect(page.getByTestId("sa-status")).toHaveText("Active");

  // The Role changes with If-Match.
  await page.getByLabel("Role").selectOption({ label: "Viewer" });
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("sa-role")).toHaveText("Viewer");
  await expect(page.getByText("Saved.")).toBeVisible();

  await page.goto("/admin/audit-log?action=user.created");
  const newest = page.getByRole("table", { name: "Audit log" }).getByRole("row").nth(1);
  await expect(newest.getByTestId("audit-actor")).toHaveText(
    "terraform (service account) · token ci",
  );
  await shot(page, "service-account-audit-log");

  // The Role change names its field.
  await page.goto("/admin/audit-log?action=service_account.updated");
  const updated = page.getByRole("table", { name: "Audit log" }).getByRole("row").nth(1);
  await expect(updated.getByTestId("audit-diff")).toContainText("Role:");
  await expect(updated.getByTestId("audit-diff")).not.toContainText("/role");

  // The list shows the Role, the status and the token count.
  await page.goto("/admin/service-accounts");
  const row = accountRow(page, "terraform");
  await expect(row.getByRole("cell").nth(1)).toHaveText("Viewer");
  await expect(row.getByRole("cell").nth(2)).toHaveText("Active");
  await expect(row.getByRole("cell").nth(3)).toHaveText("1");
  await shot(page, "service-accounts-list");
  await page.setViewportSize({ width: 360, height: 740 });
  await expectNoHorizontalScroll(page);
  await shot(page, "service-accounts-list-360");
  await page.setViewportSize({ width: 1280, height: 800 });

  // Revoke the token, then delete the account.
  await row.getByRole("link", { name: "terraform" }).click();
  await tokenRow(page, "ci").getByRole("button", { name: "Revoke" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Revoke" }).click();
  await expect(tokenRow(page, "ci")).toHaveCount(0);
  await expect(page.getByText("This account has no tokens.")).toBeVisible();
  await expect(page.getByTestId("sa-tokens")).toHaveText("0");
  await page.getByRole("button", { name: "Delete" }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm.getByRole("heading", { name: "Delete terraform?" })).toBeVisible();
  await confirm.getByRole("button", { name: "Delete" }).click();
  await expect(page).toHaveURL(`${APP}/admin/service-accounts`);
  await expect(accountRow(page, "terraform")).toHaveCount(0);
  expect(csp).toEqual([]);
});
