// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The details of a Team in a real browser (C-25.FR-3): the name, the slug with the format as its hint, and the
// description; a new Team's slug follows its name until it is edited, a slug outside the format is stopped at the field,
// a refusal of the server shows at the field it names, and without teams:write the form only shows the Team.

import { I18nextProvider } from "react-i18next";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { ApiError } from "../lib/api";
import { team } from "../test/fixtures/teams";
import { TeamForm, type TeamFormProps, slugOf } from "./team-form";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

function renderForm(props: Partial<TeamFormProps> = {}) {
  const save = vi.fn<TeamFormProps["save"]>(() => Promise.resolve());
  const queryClient = new QueryClient();
  const screen = render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <TeamForm submitLabel="Save" save={save} {...props} />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { save, screen };
}

const name = () => page.getByLabelText("Name", { exact: true });
const slug = () => page.getByLabelText("Slug", { exact: true });

describe("the Team form", () => {
  test("derives a slug of the allowed format from a name", () => {
    expect(slugOf("Payments")).toBe("payments");
    expect(slugOf("  Search & Ads team ")).toBe("search-ads-team");
    expect(slugOf("--Платежи")).toBe("");
    expect(slugOf("a".repeat(80))).toHaveLength(63);
  });

  test("a new Team's slug follows its name until the slug is edited, and Save sends the details", async () => {
    const { save } = renderForm();
    await expect
      .element(
        page.getByText(
          "The value of the Team label that names the Team: 1 to 63 characters, lowercase letters, digits, - _ and ., starting with a letter or a digit.",
        ),
      )
      .toBeVisible();
    await userEvent.fill(name(), "Payments Team");
    await expect.element(slug()).toHaveValue("payments-team");
    await userEvent.fill(slug(), "payments");
    await userEvent.fill(name(), "Payments EU");
    await expect.element(slug()).toHaveValue("payments");
    await userEvent.fill(page.getByLabelText("Description", { exact: true }), "Card payments");
    await page.getByRole("button", { name: "Save" }).click();
    await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]?.[0]).toEqual({
      name: "Payments EU",
      slug: "payments",
      description: "Card payments",
    });
  });

  test("stops an empty name and a slug outside the format at the field", async () => {
    const { save } = renderForm();
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("Fill in this field.").first()).toBeVisible();
    expect(name().element().getAttribute("aria-invalid")).toBe("true");
    await userEvent.fill(name(), "x");
    await userEvent.fill(slug(), "Not A Slug");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("This value has the wrong format.")).toBeVisible();
    expect(slug().element().getAttribute("aria-invalid")).toBe("true");
    expect(save).not.toHaveBeenCalled();
  });

  test("shows a taken name and a taken slug at their fields", async () => {
    const taken = vi.fn<TeamFormProps["save"]>(() =>
      Promise.reject(new ApiError(409, { code: "name_taken" }, undefined)),
    );
    renderForm({ save: taken, team: team() });
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("Another Team has this name.")).toBeVisible();
    expect(name().element().getAttribute("aria-invalid")).toBe("true");
    taken.mockImplementation(() =>
      Promise.reject(
        new ApiError(
          409,
          { code: "slug_taken", errors: [{ pointer: "/slug", code: "slug_taken" }] },
          undefined,
        ),
      ),
    );
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("Another Team has this slug.")).toBeVisible();
  });

  test("shows the Team without teams:write and reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    renderForm({
      team: team({ name: "Платежи", slug: "payments", description: "Карты" }),
      readOnly: true,
    });
    await expect.element(page.getByLabelText("Название", { exact: true })).toHaveValue("Платежи");
    await expect.element(page.getByLabelText("Название", { exact: true })).toBeDisabled();
    await expect.element(page.getByLabelText("Slug", { exact: true })).toHaveValue("payments");
    expect(page.getByRole("button").elements()).toHaveLength(0);
  });
});
