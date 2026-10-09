// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Secrets and the Signing secret of an outgoing webhook in a real browser: the list of names with "Set", the time
// of the change and the reference, never a value; "Add secret", "Replace" and "Remove" with the list's ETag as
// If-Match, a value field that starts empty and is gone after saving, and the value kept out of every cache; the
// refusals of a bad name, a taken name and a stale list; a Viewer sees the names only. The Signing secret section shows
// its status; "Regenerate" asks first, then shows the new secret once with a copy button, kept out of every cache;
// while the previous secret still signs it says so with "Retire previous secret". Both in English and Russian.

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
import { page, userEvent } from "vitest/browser";
import { render } from "vitest-browser-react";

import type {
  DestinationSecret,
  Permission,
  Session,
  SigningSecretStatus,
  WebhookDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { Route as NewDestinationRoute } from "../routes/destinations.new";
import { DestinationSecrets } from "./destination-secrets";
import { defaultMentions } from "./mention-settings";
import { SigningSecret } from "./signing-secret";

const ID = "DS0000000000WH";
const SECRET_VALUE = "s3cr3t-value";
const SIGNING = "whsec_bmV3IHNpZ25pbmcgc2VjcmV0IGZvciB0ZXN0cw==";

const DESTINATION: WebhookDestination = {
  id: ID,
  type: "webhook",
  name: "automation",
  mode: "events",
  events: { url: "http://127.0.0.1:18093/hook/ops", headers: [] },
  mentions: defaultMentions(),
  limiter: { limit: 5, per_seconds: 1 },
  proxy: { enabled: false },
  signing_secret_status: { set: true, updated_at: "2026-10-09T09:30:00Z" },
  warnings: [],
  health: { state: "healthy" },
  routes: [],
  created_at: "2026-10-09T09:30:00Z",
  etag: '"4"',
};

const WRITE: Permission[] = ["destinations:read", "destinations:write"];
const READ: Permission[] = ["destinations:read"];

interface Call {
  method: string;
  url: string;
  ifMatch: string | null;
  body: string;
}

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

/** A fake API of the Secrets and the Signing secret of one Destination; answers can be scripted per call. */
function fakeApi(
  options: { secrets?: DestinationSecret[]; previous?: boolean; refuse?: Response } = {},
) {
  const calls: Call[] = [];
  let version = 4;
  const secrets: DestinationSecret[] = [...(options.secrets ?? [])];
  let status: SigningSecretStatus = {
    set: true,
    updated_at: "2026-10-09T09:30:00Z",
    previous_active_since: options.previous === true ? "2026-10-09T10:00:00Z" : null,
  };
  let refuse = options.refuse;
  vi.spyOn(window, "fetch").mockImplementation(async (input, init) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = (init?.method ?? "GET").toUpperCase();
    const body = typeof init?.body === "string" ? init.body : "";
    calls.push({ method, url, ifMatch: new Headers(init?.headers).get("If-Match"), body });
    const path = url.replace(/\?.*$/, "");
    if (method !== "GET" && refuse !== undefined) {
      const answer = refuse;
      refuse = undefined;
      return answer;
    }
    if (path === `/api/v1/destinations/${ID}/secrets` && method === "GET") {
      return json(200, { items: secrets }, { ETag: `"${version}"` });
    }
    const named = /\/secrets\/([^/]+)$/.exec(path);
    if (named !== null && method === "PUT") {
      const name = decodeURIComponent(named[1] ?? "");
      const item = { name, set: true, updated_at: "2026-10-09T11:00:00Z" };
      const at = secrets.findIndex((s) => s.name === name);
      if (at < 0) {
        secrets.push(item);
      } else {
        secrets[at] = item;
      }
      version += 1;
      return json(200, item, { ETag: `"${version}"` });
    }
    if (named !== null && method === "DELETE") {
      const name = decodeURIComponent(named[1] ?? "");
      secrets.splice(
        secrets.findIndex((s) => s.name === name),
        1,
      );
      version += 1;
      return new Response(null, { status: 204 });
    }
    if (path === `/api/v1/destinations/${ID}/signing-secret` && method === "GET") {
      return json(200, status);
    }
    if (path === `/api/v1/destinations/${ID}/signing-secret` && method === "POST") {
      status = {
        set: true,
        updated_at: "2026-10-09T11:00:00Z",
        previous_active_since: "2026-10-09T11:00:00Z",
      };
      return json(201, { secret: SIGNING, status });
    }
    if (path === `/api/v1/destinations/${ID}/signing-secret/previous` && method === "DELETE") {
      status = { ...status, previous_active_since: null };
      return new Response(null, { status: 204 });
    }
    if (path === `/api/v1/destinations/${ID}`) {
      return json(200, { ...DESTINATION, etag: `"${version}"`, signing_secret_status: status });
    }
    return json(404, { title: "not mocked" });
  });
  return calls;
}

function providers(children: ReactNode, permissions: Permission[] = WRITE) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the sections read only the Permissions
      user: { id: "SR0000000000AA", name: "Admin", time_zone: "UTC" } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-09T09:00:00Z",
      idle_expires_at: "2026-10-09T09:00:00Z",
      method: "local",
      permissions,
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return {
    queryClient,
    ui: (
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </I18nextProvider>
    ),
  };
}

/**
 * Presses a button of a modal dialog with the keyboard: the pointer that opened the dialog is still over its inert
 * layer.
 */
async function press(button: ReturnType<typeof page.getByRole>): Promise<void> {
  const element = button.element();
  if (element instanceof HTMLElement) {
    element.focus();
  }
  await userEvent.keyboard("{Enter}");
}

/** Everything the query and mutation caches hold, as text. */
function cached(queryClient: QueryClient): string {
  return JSON.stringify([
    queryClient
      .getQueryCache()
      .getAll()
      .map((q) => q.state.data),
    queryClient
      .getMutationCache()
      .getAll()
      .map((m) => [m.state.variables, m.state.data]),
  ]);
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("DestinationSecrets", () => {
  test("adds a Secret with the list's ETag, shows its name, Set and the date, and never its value", async () => {
    const calls = fakeApi();
    const { queryClient, ui } = providers(<DestinationSecrets destinationId={ID} />);
    await render(ui);
    await expect.element(page.getByText("No Secrets yet.")).toBeVisible();
    await page.getByRole("button", { name: "Add secret" }).click();
    const name = page.getByLabelText("Name", { exact: true });
    const value = page.getByLabelText("Value", { exact: true });
    await expect.element(value).toHaveValue("");
    await expect.element(value).toHaveAttribute("type", "password");
    await expect.element(page.getByText("Reference it as {{ .Secrets.<name> }}.")).toBeVisible();
    await userEvent.fill(name, "token");
    await expect.element(page.getByText("Reference it as {{ .Secrets.token }}.")).toBeVisible();
    await userEvent.fill(value, SECRET_VALUE);
    await page.getByRole("button", { name: "Save" }).click();

    await expect.element(page.getByTestId("secret-name")).toHaveTextContent("token");
    await expect
      .poll(() => page.getByTestId("secret-status").element().textContent)
      .toMatch(/^Set, changed .*2026/);
    await expect
      .element(page.getByTestId("secret-reference"))
      .toHaveTextContent("{{ .Secrets.token }}");
    await expect.element(page.getByTestId("secret-form")).not.toBeInTheDocument();
    await expect.element(page.getByRole("button", { name: "Add secret" })).toHaveFocus();
    const put = calls.find((c) => c.method === "PUT");
    expect(put).toEqual({
      method: "PUT",
      url: `/api/v1/destinations/${ID}/secrets/token`,
      ifMatch: '"4"',
      body: JSON.stringify({ value: SECRET_VALUE }),
    });
    expect(document.body.innerHTML).not.toContain(SECRET_VALUE);
    for (const input of document.querySelectorAll("input")) {
      expect(input.value).not.toBe(SECRET_VALUE);
    }
    expect(cached(queryClient)).not.toContain(SECRET_VALUE);

    // Replace opens an empty value field; the next change sends the list's new ETag.
    await page.getByRole("button", { name: "Replace token" }).click();
    await expect.element(page.getByLabelText("Value", { exact: true })).toHaveValue("");
    await userEvent.fill(page.getByLabelText("Value", { exact: true }), "another");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByTestId("secret-form")).not.toBeInTheDocument();
    await expect.element(page.getByRole("button", { name: "Replace token" })).toHaveFocus();
    expect(calls.filter((c) => c.method === "PUT").at(-1)?.ifMatch).toBe('"5"');

    // Remove asks first.
    await page.getByRole("button", { name: "Remove token" }).click();
    await expect
      .element(
        page
          .getByRole("dialog")
          .getByText(
            "Templates that read {{ .Secrets.token }} fail until a Secret with this name is set again.",
          ),
      )
      .toBeVisible();
    await press(page.getByRole("dialog").getByRole("button", { name: "Remove" }));
    await expect.element(page.getByText("No Secrets yet.")).toBeVisible();
    expect(calls.find((c) => c.method === "DELETE")).toMatchObject({
      url: `/api/v1/destinations/${ID}/secrets/token`,
      ifMatch: '"6"',
    });
  });

  test("refuses a bad or taken name, and offers to reload a list changed meanwhile", async () => {
    const calls = fakeApi({
      secrets: [{ name: "token", set: true, updated_at: "2026-10-09T09:30:00Z" }],
      refuse: json(412, { title: "stale", type: "precondition-failed" }),
    });
    const { ui } = providers(<DestinationSecrets destinationId={ID} />);
    await render(ui);
    await expect.element(page.getByTestId("secret-name")).toHaveTextContent("token");
    await page.getByRole("button", { name: "Add secret" }).click();
    const name = page.getByLabelText("Name", { exact: true });
    await userEvent.fill(name, "1st");
    await userEvent.fill(page.getByLabelText("Value", { exact: true }), "x");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(name)
      .toHaveAccessibleDescription(
        "Reference it as {{ .Secrets.<name> }}. Start with a letter or an underscore, then letters, digits and underscores.",
      );
    await userEvent.fill(name, "token");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByText("A Secret with this name exists: replace its value instead."))
      .toBeVisible();
    await page.getByRole("button", { name: "Save" }).click();
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(0);
    await userEvent.fill(page.getByLabelText("Value", { exact: true }), "");
    await page.getByRole("button", { name: "Save" }).click();
    await userEvent.fill(name, "other");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(page.getByText("Fill in this field.")).toBeVisible();
    await userEvent.fill(page.getByLabelText("Value", { exact: true }), "x");
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(
        page
          .getByTestId("secrets-stale")
          .getByText("The Secrets changed meanwhile. Reload to see them."),
      )
      .toBeVisible();
    const reads = calls.filter((c) => c.method === "GET").length;
    await page.getByRole("button", { name: "Reload" }).click();
    await expect.poll(() => calls.filter((c) => c.method === "GET").length).toBe(reads + 1);
  });

  test("shows a Viewer the names only, with no actions", async () => {
    fakeApi({ secrets: [{ name: "token", set: true, updated_at: "2026-10-09T09:30:00Z" }] });
    const { ui } = providers(<DestinationSecrets destinationId={ID} />, READ);
    await render(ui);
    await expect.element(page.getByTestId("secret-name")).toHaveTextContent("token");
    await expect.element(page.getByRole("button", { name: "Add secret" })).not.toBeInTheDocument();
    await expect
      .element(page.getByRole("button", { name: "Replace token" }))
      .not.toBeInTheDocument();
    await expect
      .element(page.getByRole("button", { name: "Remove token" }))
      .not.toBeInTheDocument();
  });
});

describe("SigningSecret", () => {
  test("regenerates after asking, shows the new secret once, and retires the previous one", async () => {
    const calls = fakeApi();
    const { queryClient, ui } = providers(<SigningSecret destination={DESTINATION} />);
    await render(ui);
    await expect
      .poll(() => page.getByTestId("signing-secret-status").element().textContent)
      .toMatch(/^Signing secret: set, changed .*2026/);
    await expect.element(page.getByTestId("signing-secret-previous")).not.toBeInTheDocument();
    await page.getByRole("button", { name: "Regenerate" }).click();
    await expect
      .poll(() => page.getByRole("dialog").element().textContent)
      .toMatch(/Requests carry signatures with the new and the previous secret/);
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await press(page.getByRole("dialog").getByRole("button", { name: "Regenerate" }));

    const dialog = page.getByTestId("signing-secret-dialog");
    await expect.element(dialog.getByLabelText("Secret")).toHaveValue(SIGNING);
    await expect.element(dialog.getByText("You will not see this secret again.")).toBeVisible();
    await expect.element(dialog.getByRole("button", { name: "Copy" })).toBeVisible();
    expect(cached(queryClient)).not.toContain(SIGNING);
    await press(dialog.getByRole("button", { name: "Close" }));
    await expect.element(dialog).not.toBeInTheDocument();
    await expect.element(page.getByRole("button", { name: "Regenerate" })).toHaveFocus();
    expect(document.body.innerHTML).not.toContain(SIGNING);

    const previous = page.getByTestId("signing-secret-previous");
    await expect
      .poll(() => previous.element().textContent)
      .toMatch(/^The previous secret still signs, since .*2026/);
    await previous.getByRole("button", { name: "Retire previous secret" }).click();
    await expect.element(previous).not.toBeInTheDocument();
    expect(calls.filter((c) => c.method !== "GET").map((c) => [c.method, c.url])).toEqual([
      ["POST", `/api/v1/destinations/${ID}/signing-secret`],
      ["DELETE", `/api/v1/destinations/${ID}/signing-secret/previous`],
    ]);
  });

  test("shows a Viewer the status and the warning, with no actions", async () => {
    fakeApi({ previous: true });
    const { ui } = providers(<SigningSecret destination={DESTINATION} />, READ);
    await render(ui);
    await expect.element(page.getByTestId("signing-secret-previous")).toBeVisible();
    await expect.element(page.getByRole("button", { name: "Regenerate" })).not.toBeInTheDocument();
    await expect
      .element(page.getByRole("button", { name: "Retire previous secret" }))
      .not.toBeInTheDocument();
  });

  test("speaks Russian", async () => {
    await i18n.changeLanguage("ru");
    fakeApi({
      previous: true,
      secrets: [{ name: "token", set: true, updated_at: "2026-10-09T09:30:00Z" }],
    });
    const { ui } = providers(
      <>
        <DestinationSecrets destinationId={ID} />
        <SigningSecret destination={DESTINATION} />
      </>,
    );
    await render(ui);
    await expect
      .poll(() => page.getByTestId("signing-secret-status").element().textContent)
      .toMatch(/^Секрет подписи: задан, изменён/);
    await expect
      .poll(() => page.getByTestId("signing-secret-previous").element().textContent)
      .toMatch(/^Прежний секрет всё ещё подписывает запросы, с /);
    await expect
      .poll(() => page.getByTestId("secret-status").element().textContent)
      .toMatch(/^Задан, изменён/);
    await expect.element(page.getByRole("button", { name: "Добавить секрет" })).toBeVisible();
  });
});

describe("Creating an outgoing webhook", () => {
  test("shows its Signing secret once, keeps it out of every cache, and then opens its page", async () => {
    const created = { ...DESTINATION, id: "DS0000000000NW", name: "created" };
    vi.spyOn(window, "fetch").mockImplementation(async (input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url === "/api/v1/destinations" && init?.method === "POST") {
        return json(201, { destination: created, signing_secret: SIGNING });
      }
      if (url.startsWith("/api/v1/user-directory")) {
        return json(200, { items: [], next_cursor: null });
      }
      return json(404, { title: "not mocked" });
    });
    const Page = NewDestinationRoute.options.component;
    expect(Page).toBeDefined();
    const rootRoute = createRootRoute({ component: () => (Page === undefined ? null : <Page />) });
    const router = createRouter({
      routeTree: rootRoute,
      history: createMemoryHistory({ initialEntries: ["/destinations/new"] }),
    });
    const { queryClient, ui } = providers(<RouterProvider router={router} />);
    await render(ui);
    await page.getByRole("radio", { name: "Outgoing webhook" }).click();
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "created");
    await userEvent.fill(
      page.getByLabelText("URL", { exact: true }),
      "http://127.0.0.1:18093/hook/x",
    );
    await page.getByRole("button", { name: "Save", exact: true }).click();
    const dialog = page.getByTestId("signing-secret-dialog");
    await expect.element(dialog.getByLabelText("Secret")).toHaveValue(SIGNING);
    expect(cached(queryClient)).not.toContain(SIGNING);
    expect(cached(queryClient)).toContain("DS0000000000NW");
    await press(dialog.getByRole("button", { name: "Close" }));
    await expect.poll(() => router.state.location.pathname).toBe("/destinations/DS0000000000NW");
    expect(document.body.innerHTML).not.toContain(SIGNING);
    expect(cached(queryClient)).not.toContain(SIGNING);
  });
});
