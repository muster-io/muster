// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Lookup table editor in a real browser: the grid of a key column and named columns, rows and columns added and
// removed, the rows a save sends, the errors it finds itself, a refusal landing on its column, key, cell or row
// (column_mismatch), a rename refused while a Link rule reads the table (in_use, with the rules its link_rules names), a
// newer version
// (412), and the read-only table without lookup-tables:write.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { LookupTable, LookupTableBase, Permission, Session } from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead, apiFetch } from "../lib/api";
import {
  LookupTableEditor,
  addColumn,
  addRow,
  checkGrid,
  entriesOf,
  gridOf,
  removeColumn,
  serverErrors,
} from "./lookup-table-editor";

const TABLE: LookupTable = {
  id: "TB0000000000AA",
  name: "grafana",
  description: "Grafana of each cluster",
  columns: ["address", "datasource_uid"],
  entries: [
    { key: "prod", values: { address: "https://grafana.example.org", datasource_uid: "PROM1" } },
  ],
  created_at: "2026-10-08T09:00:00Z",
  etag: '"3"',
};

function refusal(status: number, problem: Record<string, unknown>): ApiError {
  return new ApiError(
    status,
    { type: "https://muster-io.github.io/muster/problems/x", ...problem },
    undefined,
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the grid", () => {
  test("keeps the values of each row aligned with the columns as columns come and go", () => {
    let grid = gridOf(TABLE);
    grid = addColumn(grid);
    grid.columns[2]!.name = "team";
    grid = addRow(grid);
    grid.rows[1]!.key = " stage ";
    grid.rows[1]!.values = ["https://grafana-stage.example.org", "PROM2", "ops"];
    grid = removeColumn(grid, grid.columns[1]!.id);
    expect(grid.columns.map((c) => c.name)).toEqual(["address", "team"]);
    expect(entriesOf(grid)).toEqual([
      { key: "prod", values: { address: "https://grafana.example.org", team: "" } },
      { key: "stage", values: { address: "https://grafana-stage.example.org", team: "ops" } },
    ]);
  });

  test("finds empty and repeated names and keys", () => {
    const grid = addRow(addRow(addColumn(gridOf(TABLE))));
    grid.columns[2]!.name = "address";
    grid.rows[1]!.key = "prod";
    const errors = checkGrid(" ", grid);
    expect(errors.name).toBe("required");
    expect(errors[`column:${grid.columns[2]!.id}`]).toBe("duplicate");
    expect(errors[`key:${grid.rows[1]!.id}`]).toBe("duplicate");
    expect(errors[`key:${grid.rows[2]!.id}`]).toBe("required");
    expect(checkGrid("grafana", gridOf(TABLE))).toEqual({});
  });

  test("puts the errors of a refusal on the column, key, cell or row the request was sent with", () => {
    const grid = addRow(gridOf({ ...TABLE, columns: ["address", "a/b"] }));
    const err = refusal(422, {
      errors: [
        { pointer: "/columns/1", code: "duplicate" },
        { pointer: "/entries/0/values", code: "column_mismatch" },
        { pointer: "/entries/1/key", code: "invalid_format" },
        { pointer: "/entries/1/values/a~1b", code: "too_long" },
        { pointer: "/entries", code: "too_long" },
      ],
    });
    const [prod, added] = grid.rows;
    expect(serverErrors(err, grid)).toEqual({
      [`column:${grid.columns[1]!.id}`]: "duplicate",
      [`row:${prod!.id}`]: "column_mismatch",
      [`key:${added!.id}`]: "invalid_format",
      [`cell:${added!.id}:${grid.columns[1]!.id}`]: "too_long",
      entries: "too_long",
    });
  });
});

describe("the in_use refusal", () => {
  test("carries the Link rules of link_rules and leaves out malformed items", async () => {
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          type: "https://muster-io.github.io/muster/problems/conflict",
          title: "Conflict",
          status: 409,
          code: "in_use",
          link_rules: [{ id: "KR0000000000AA", name: "Explore" }, { id: 1 }, "Dashboard"],
        }),
        { status: 409, headers: { "Content-Type": "application/problem+json" } },
      ),
    );
    const err: unknown = await apiFetch("/api/v1/lookup-tables/TB0000000000AA", {
      method: "DELETE",
    }).catch((e: unknown) => e);
    if (!(err instanceof ApiError)) {
      throw new Error("not an ApiError");
    }
    expect(err.code).toBe("in_use");
    expect(err.link_rules).toEqual([{ id: "KR0000000000AA", name: "Explore" }]);
  });
});

async function renderEditor(
  save: (input: LookupTableBase) => Promise<unknown>,
  permissions: Permission[] = ["lookup-tables:read", "lookup-tables:write"],
  readOnly = false,
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the editor reads only the Permissions
      user: { id: "SR0000000000AA", name: "Admin" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-09T09:00:00Z",
      idle_expires_at: "2026-10-09T09:00:00Z",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <LookupTableEditor
          table={TABLE}
          readOnly={readOnly}
          submitLabel="Save"
          save={save}
          onReload={() => {}}
          onCancel={() => {}}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
}

describe("LookupTableEditor", () => {
  test("adds a row and saves the rows of the grid", async () => {
    const save = vi.fn((_input: LookupTableBase) => Promise.resolve());
    await renderEditor(save);
    await expect.element(page.getByLabelText("Key of row 1")).toHaveValue("prod");
    await page.getByRole("button", { name: "Add row" }).click();
    await expect.element(page.getByLabelText("Key of row 2")).toHaveFocus();
    await userEvent.fill(page.getByLabelText("Key of row 2"), "stage");
    await userEvent.fill(
      page.getByLabelText("address of row stage"),
      "https://grafana-stage.example.org",
    );
    await userEvent.fill(page.getByLabelText("datasource_uid of row stage"), "PROM2");
    await expect.element(page.getByTestId("lookup-row-count")).toHaveTextContent("2 rows");
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      name: "grafana",
      description: "Grafana of each cluster",
      columns: ["address", "datasource_uid"],
      entries: [
        TABLE.entries[0],
        {
          key: "stage",
          values: { address: "https://grafana-stage.example.org", datasource_uid: "PROM2" },
        },
      ],
    });
  });

  test("adds and removes columns and rows in the grid", async () => {
    const save = vi.fn((_input: LookupTableBase) => Promise.resolve());
    await renderEditor(save);
    await page.getByRole("button", { name: "Add column" }).click();
    await expect.element(page.getByLabelText("Name of column 3")).toHaveFocus();
    await userEvent.fill(page.getByLabelText("Name of column 3"), "team");
    await userEvent.fill(page.getByLabelText("team of row prod"), "ops");
    await page.getByRole("button", { name: "Remove column datasource_uid" }).click();
    await expect.element(page.getByLabelText("Name of column 2")).toHaveValue("team");
    await page.getByRole("button", { name: "Add row" }).click();
    await userEvent.fill(page.getByLabelText("Key of row 2"), "stage");
    await page.getByRole("button", { name: "Remove row prod" }).click();
    await expect.element(page.getByLabelText("Key of row 1")).toHaveValue("stage");
    await expect.element(page.getByLabelText("Key of row 1")).toHaveFocus();
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      name: "grafana",
      description: "Grafana of each cluster",
      columns: ["address", "team"],
      entries: [{ key: "stage", values: { address: "", team: "" } }],
    });
  });

  test("refuses a row without a key before it sends anything", async () => {
    const save = vi.fn(() => Promise.resolve());
    await renderEditor(save);
    await page.getByRole("button", { name: "Add row" }).click();
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("Enter a key.")).toBeVisible();
    await expect.element(page.getByLabelText("Key of row 2")).toHaveFocus();
    expect(save).not.toHaveBeenCalled();
  });

  test("marks the row of a column_mismatch refusal", async () => {
    await renderEditor(() =>
      Promise.reject(
        refusal(422, { errors: [{ pointer: "/entries/0/values", code: "column_mismatch" }] }),
      ),
    );
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByText("The values of this row do not match the columns."))
      .toBeVisible();
    await expect.element(page.getByTestId("lookup-row")).toHaveAttribute("data-invalid", "true");
    await expect
      .element(page.getByLabelText("Key of row 1"))
      .toHaveAttribute("aria-invalid", "true");
  });

  test("names the Link rules of the refusal when a rename is refused, without reading the rules", async () => {
    const fetch = vi.spyOn(window, "fetch");
    await renderEditor(() =>
      Promise.reject(
        refusal(409, {
          code: "in_use",
          link_rules: [
            { id: "KR0000000000AA", name: "Explore" },
            { id: "KR0000000000BB", name: "Dashboard" },
          ],
        }),
      ),
    );
    await userEvent.fill(page.getByLabelText("Name"), "grafana-prod");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("lookup-table-in-use"))
      .toHaveTextContent(
        "This table is used by a Link rule and cannot be renamed. Link rules that read it: Explore, Dashboard.",
      );
    expect(fetch).not.toHaveBeenCalled();
  });

  test("says only the sentence of the action when the refusal names no rules", async () => {
    await renderEditor(() => Promise.reject(refusal(409, { code: "in_use" })));
    await userEvent.fill(page.getByLabelText("Name"), "grafana-prod");
    await page.getByRole("button", { name: "Save" }).click();
    const text = page.getByTestId("lookup-table-in-use");
    await expect
      .element(text)
      .toHaveTextContent("This table is used by a Link rule and cannot be renamed.");
    expect(text.element().textContent).toBe(
      "This table is used by a Link rule and cannot be renamed.",
    );
  });

  test("offers a reload when someone else changed the table", async () => {
    await renderEditor(() => Promise.reject(refusal(412, {})));
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByText("Someone else changed this Lookup table. Reload to see the changes."))
      .toBeVisible();
    await expect.element(page.getByRole("button", { name: "Reload" })).toBeVisible();
  });

  test("shows the table without inputs when it cannot be changed, in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderEditor(() => Promise.resolve(), ["lookup-tables:read"], true);
    await expect
      .element(page.getByText("Эту таблицу соответствий можно смотреть, но не менять."))
      .toBeVisible();
    await expect
      .element(page.getByRole("cell", { name: "https://grafana.example.org" }))
      .toBeVisible();
    expect(document.querySelectorAll("input").length).toBe(0);
    expect(page.getByRole("button", { name: "Сохранить" }).query()).toBeNull();
  });
});
