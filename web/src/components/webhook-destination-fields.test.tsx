// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Destination form with the outgoing webhook fields in a real browser: the mode tabs "Events", "Template" and
// "Both" with no Connection and no "Check"; the URL and headers of the events mode; the four request builders of the
// template mode with the extraction rules of "Create" and "Open thread"; the request a save sends in each mode; a
// template refused on save marked at its field with its line and column, and the refused reads of .Secrets; the
// literal-credential warning at its field while it holds the stored value; the form of a Viewer, read-only with the
// .Secrets references; the delete dialog's text by mode; and the fields in Russian.

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
  DestinationInput,
  Permission,
  ProblemError,
  Session,
  WebhookDestination,
} from "../api/gen/model";
import i18n from "../i18n";
import { ApiError, SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { DestinationDeleteDialog, deleteDescription } from "./destination-delete-dialog";
import { DestinationForm, versionAction } from "./destination-form";
import { shownProblem } from "./header-editor";
import { defaultMentions } from "./mention-settings";
import {
  WEBHOOK_KIND,
  responseRefs,
  unreadableValue,
  valueAt,
  webhookChanged,
  webhookErrors,
  webhookRequests,
  webhookValues,
} from "./webhook-destination-fields";

const LITERAL =
  "Store credentials as Secrets: this value is shown to everyone who can read Destinations.";

const DESTINATION: WebhookDestination = {
  id: "DS0000000000WH",
  type: "webhook",
  name: "automation",
  mode: "events",
  events: {
    url: "http://127.0.0.1:18093/hook/ops",
    headers: [
      { name: "Authorization", value: "Bearer abc" },
      { name: "X-Team", value: "ops" },
    ],
  },
  mentions: defaultMentions(),
  limiter: { limit: 5, per_seconds: 1 },
  proxy: { enabled: false },
  signing_secret_status: { set: true, updated_at: "2026-10-09T09:30:00Z" },
  warnings: [{ kind: "literal_credential", field: "/events/headers/0/value" }],
  health: { state: "healthy" },
  routes: [],
  created_at: "2026-10-09T09:30:00Z",
  etag: '"4"',
};

const TEMPLATE_DESTINATION: WebhookDestination = {
  ...DESTINATION,
  mode: "template",
  events: undefined,
  template: {
    create: {
      method: "POST",
      url: "http://127.0.0.1:18093/chat/ops/messages",
      headers: [{ name: "Authorization", value: "Bearer {{ .Secrets.token }}" }],
      body: '{"text": {{ .AlertGroup.Title | toJson }}}',
      extract: [{ name: "id", path: "$.data.id" }],
    },
    update: {
      method: "PUT",
      url: "http://127.0.0.1:18093/chat/ops/messages/{{ .Response.id }}",
      headers: [],
      body: null,
    },
  },
  warnings: [],
};

const WRITE: Permission[] = ["destinations:read", "destinations:write"];
const READ: Permission[] = ["destinations:read"];

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function refused(...errors: ProblemError[]): ApiError {
  return new ApiError(
    422,
    {
      type: "https://muster-io.github.io/muster/problems/validation-failed",
      code: "validation_failed",
      errors,
    },
    undefined,
  );
}

function mockReads() {
  return vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("/api/v1/user-directory")) {
      return Promise.resolve(json(200, { items: [], next_cursor: null }));
    }
    return Promise.resolve(json(404, { title: "not mocked" }));
  });
}

function providers(children: ReactNode, permissions: Permission[] = WRITE) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the form reads only the Permissions
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
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  );
}

function builder(name: string) {
  return page.getByRole("group", { name, exact: true });
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
});

describe("the outgoing webhook type", () => {
  test("starts in events mode with POST and PUT requests, the webhook limiter and everyone as data", () => {
    const v = webhookValues(undefined);
    expect(v.mode).toBe("events");
    expect(v.events).toEqual({ url: "", headers: [] });
    expect(v.requests.create.method).toBe("POST");
    expect(v.requests.update.method).toBe("PUT");
    expect(v.added).toEqual({ open_thread: false, reply_in_thread: false });
    expect(WEBHOOK_KIND.defaultLimiter).toEqual({ limit: 5, per_seconds: 1 });
    expect(WEBHOOK_KIND.everyone).toEqual(["all"]);
    expect(WEBHOOK_KIND.groups).toBe(true);
  });

  test("checks the URLs, header names and extraction rules of the requests the mode sends", () => {
    const v = webhookValues(undefined);
    v.events.headers = [{ name: " ", value: "x" }];
    expect(webhookErrors(v)).toEqual({
      "/events/url": "required",
      "/events/headers/0/name": "required",
    });
    v.mode = "template";
    v.requests.create.extract = [{ name: "", path: "" }];
    v.added.open_thread = true;
    expect(webhookErrors(v)).toEqual({
      "/template/create/url": "required",
      "/template/create/extract/0/name": "required",
      "/template/create/extract/0/path": "required",
      "/template/update/url": "required",
      "/template/open_thread/url": "required",
    });
    v.mode = "both";
    v.proxy.enabled = true;
    expect(Object.keys(webhookErrors(v))).toContain("/events/url");
    expect(webhookErrors(v)["/proxy/address"]).toBe("required");
  });

  test("sends the requests of its mode only, the added optional ones, and extraction rules where they belong", () => {
    const v = webhookValues(TEMPLATE_DESTINATION);
    expect(webhookRequests(v)).toEqual({ template: TEMPLATE_DESTINATION.template });
    v.added.reply_in_thread = true;
    v.requests.reply_in_thread.url =
      " http://127.0.0.1:18093/chat/ops/messages/{{ .Response.id }}/replies ";
    v.requests.reply_in_thread.extract = [{ name: "ignored", path: "$.x" }];
    expect(webhookRequests(v).template?.reply_in_thread).toEqual({
      method: "POST",
      url: "http://127.0.0.1:18093/chat/ops/messages/{{ .Response.id }}/replies",
      headers: [],
      body: null,
    });
    v.mode = "events";
    expect(webhookRequests(v)).toEqual({ events: { url: "", headers: [] } });
    expect(valueAt(webhookRequests(webhookValues(DESTINATION)), "/events/headers/0/value")).toBe(
      "Bearer abc",
    );
    expect(valueAt({ a: [1] }, "/a/1")).toBeUndefined();
    expect(valueAt("x", "/a")).toBeUndefined();
  });

  test("names the refused reads of .Secrets and the codes of the request fields in the reader's language", () => {
    const t = i18n.t.bind(i18n);
    expect(
      shownProblem(t, "/events/url", {
        code: "invalid_format",
        detail: "Read a Secret as …",
        line: 1,
        column: 12,
      }),
    ).toEqual({
      pointer: "/events/url",
      code: "invalid_format",
      line: 1,
      column: 12,
      detail:
        "Read a Secret as {{ .Secrets.<name> }} and an extracted value as {{ .Response.<name> }}: other uses of .Secrets and .Response are refused.",
    });
    expect(shownProblem(t, "/events/headers/0/name", { code: "reserved" }).detail).toBe(
      "Muster sets this header itself.",
    );
    expect(
      shownProblem(t, "/events/headers/0/name", { code: "invalid_format", detail: "x" }).detail,
    ).toBe("Enter a valid header name that no other header of this request has.");
    expect(
      shownProblem(t, "/template/create/extract/0/name", { code: "invalid_format" }).detail,
    ).toBe(
      "Start with a letter or an underscore, then letters, digits and underscores; no other rule may have this name.",
    );
    expect(
      shownProblem(t, "/template/create/extract/0/path", { code: "invalid_format" }).detail,
    ).toBe("Enter a valid JSONPath, such as $.data.id.");
    expect(
      shownProblem(t, "/template/update/url", {
        code: "invalid_format",
        detail: "The URL is not an absolute http or https URL.",
      }).detail,
    ).toBe(
      "Enter an absolute http or https URL; it must stay one once its templates are filled in.",
    );
    expect(shownProblem(t, "/events/url", { code: "required" }).detail).toBe("Fill in this field.");
    expect(
      shownProblem(t, "/template/update/body", {
        code: "template_syntax",
        detail: 'unexpected "}" in operand',
        line: 1,
        column: 8,
      }),
    ).toEqual({
      pointer: "/template/update/body",
      code: "template_syntax",
      detail: 'unexpected "}" in operand',
      line: 1,
      column: 8,
    });
  });
});

describe("DestinationForm with the outgoing webhook fields", () => {
  test("has the mode tabs, no Connection and no Check, and saves the events request", async () => {
    mockReads();
    const save = vi.fn((_input: DestinationInput) => Promise.resolve(undefined));
    await render(providers(<DestinationForm kind={WEBHOOK_KIND} submitLabel="Save" save={save} />));
    await expect
      .element(page.getByRole("tablist", { name: "Mode" }))
      .toHaveTextContent("EventsTemplateBoth");
    await expect
      .element(page.getByRole("tab", { name: "Events" }))
      .toHaveAttribute("aria-selected", "true");
    await expect.element(page.getByLabelText("Connection")).not.toBeInTheDocument();
    await expect.element(page.getByRole("button", { name: "Check" })).not.toBeInTheDocument();
    await expect.element(page.getByText("Proxy", { exact: true })).toBeVisible();
    await expect.element(page.getByLabelText("Requests")).toHaveValue("5");
    await expect.element(page.getByLabelText("Period in seconds")).toHaveValue("1");
    await expect.element(page.getByTestId("webhook-template")).not.toBeInTheDocument();
    // Mentions are data: everyone, and the receiver formats it.
    const everyone = document.querySelectorAll<HTMLSelectElement>('select[id$="-everyone"]');
    for (const select of everyone) {
      expect([...select.options].map((o) => o.value)).toEqual(["none", "all"]);
    }
    await expect
      .element(page.getByLabelText("New Alert Group", { exact: true }))
      .toHaveAccessibleDescription(
        "Muster sends mentions as data the receiver formats: everyone, groups by name and users with their login. Chosen users and groups are mentioned as well.",
      );

    // Groups are names the receiver resolves, not Mattermost groups.
    await expect.element(page.getByPlaceholder("Group name").first()).toBeVisible();
    await expect.element(page.getByPlaceholder("Mattermost group").first()).not.toBeInTheDocument();
    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "automation");
    await userEvent.fill(
      page.getByLabelText("URL", { exact: true }),
      " http://127.0.0.1:18093/hook/ops ",
    );
    await page.getByRole("button", { name: "Add header" }).click();
    await userEvent.fill(page.getByLabelText("Name of header 1"), "Authorization");
    await userEvent.fill(page.getByLabelText("Value of header 1"), "Bearer {{ .Secrets.token }}");
    await page.getByRole("button", { name: "Add header" }).click();
    await page.getByRole("button", { name: "Remove header 2" }).click();
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "webhook",
      name: "automation",
      mode: "events",
      events: {
        url: "http://127.0.0.1:18093/hook/ops",
        headers: [{ name: "Authorization", value: "Bearer {{ .Secrets.token }}" }],
      },
      proxy: { enabled: false, type: "http", username: null },
      mentions: defaultMentions(),
      limiter: { limit: 5, per_seconds: 1 },
    });
  });

  test("builds the four requests of the template mode, with extraction rules on Create and Open thread", async () => {
    mockReads();
    const save = vi.fn((_input: DestinationInput) => Promise.resolve(undefined));
    await render(providers(<DestinationForm kind={WEBHOOK_KIND} submitLabel="Save" save={save} />));
    await page.getByRole("tab", { name: "Template" }).click();
    await expect.element(page.getByTestId("webhook-events")).not.toBeInTheDocument();
    for (const name of ["Create", "Update", "Open thread", "Reply in thread"]) {
      await expect.element(builder(name)).toBeVisible();
    }
    await expect.element(builder("Create").getByText("Extraction rules")).toBeVisible();
    await expect.element(builder("Update").getByText("Extraction rules")).not.toBeInTheDocument();
    // The optional requests show their fields once added.
    await expect
      .element(builder("Open thread").getByLabelText("URL", { exact: true }))
      .not.toBeInTheDocument();
    await builder("Open thread").getByLabelText("Add “Open thread”").click();
    await expect.element(builder("Open thread").getByText("Extraction rules")).toBeVisible();
    await expect
      .element(builder("Reply in thread").getByText("Extraction rules"))
      .not.toBeInTheDocument();

    await userEvent.fill(page.getByLabelText("Name", { exact: true }), "chat");
    await userEvent.fill(
      builder("Create").getByLabelText("URL", { exact: true }),
      "http://127.0.0.1:18093/chat/ops/messages",
    );
    await userEvent.fill(
      page.getByRole("textbox", { name: "Body of Create" }),
      '{"text": {{ .AlertGroup.Title | toJson }}}',
    );
    await expect
      .element(builder("Create").getByText("Read it as {{ .Response.<name> }}.", { exact: false }))
      .toBeVisible();
    await builder("Create").getByRole("button", { name: "Add rule" }).click();
    await userEvent.fill(builder("Create").getByLabelText("Name of rule 1"), "id");
    await userEvent.fill(builder("Create").getByLabelText("JSONPath of rule 1"), "$.data.id");
    await userEvent.selectOptions(builder("Update").getByLabelText("Method"), "PATCH");
    await userEvent.fill(
      builder("Update").getByLabelText("URL", { exact: true }),
      "http://127.0.0.1:18093/chat/ops/messages/{{ .Response.id }}",
    );
    await userEvent.fill(
      builder("Open thread").getByLabelText("URL", { exact: true }),
      "http://127.0.0.1:18093/chat/ops/threads",
    );
    await page.getByRole("button", { name: "Save" }).click();
    expect(save).toHaveBeenCalledWith({
      type: "webhook",
      name: "chat",
      mode: "template",
      template: {
        create: {
          method: "POST",
          url: "http://127.0.0.1:18093/chat/ops/messages",
          headers: [],
          body: '{"text": {{ .AlertGroup.Title | toJson }}}',
          extract: [{ name: "id", path: "$.data.id" }],
        },
        update: {
          method: "PATCH",
          url: "http://127.0.0.1:18093/chat/ops/messages/{{ .Response.id }}",
          headers: [],
          body: null,
        },
        open_thread: {
          method: "POST",
          url: "http://127.0.0.1:18093/chat/ops/threads",
          headers: [],
          body: null,
          extract: [],
        },
      },
      proxy: { enabled: false, type: "http", username: null },
      mentions: defaultMentions(),
      limiter: { limit: 5, per_seconds: 1 },
    });

    // "Both" shows both parts.
    await page.getByRole("tab", { name: "Both" }).click();
    await expect.element(page.getByTestId("webhook-events")).toBeVisible();
    await expect.element(page.getByTestId("webhook-template")).toBeVisible();
  });

  test("marks a template refused on save at its field with its line and column", async () => {
    mockReads();
    const save = vi
      .fn((_input: DestinationInput) => Promise.resolve(undefined))
      .mockRejectedValueOnce(
        refused({
          pointer: "/template/update/body",
          code: "template_syntax",
          detail: 'template: /template/update/body:1: unexpected "}" in operand',
          line: 1,
          column: 8,
        }),
      )
      .mockRejectedValueOnce(
        refused({
          pointer: "/template/create/headers/0/value",
          code: "invalid_format",
          detail: "Read a Secret as …",
          line: 1,
          column: 11,
        }),
      );
    await render(
      providers(
        <DestinationForm
          kind={WEBHOOK_KIND}
          destination={TEMPLATE_DESTINATION}
          submitLabel="Save"
          save={save}
        />,
      ),
    );
    const body = page.getByRole("textbox", { name: "Body of Update" });
    await userEvent.fill(body, "{{ .Nope }");
    await page.getByRole("button", { name: "Save" }).click();
    await expect.element(body).toHaveAttribute("aria-invalid", "true");
    await expect.element(body).toHaveFocus();
    await expect
      .element(builder("Update").getByTestId("template-errors"))
      .toHaveTextContent(
        'Line 1, column 8: template: /template/update/body:1: unexpected "}" in operand',
      );
    await expect.element(page.getByText("Fix the marked fields and save again.")).toBeVisible();
    // Fixing the body clears the mark; the next refusal names the Secret read by an expression.
    await userEvent.fill(body, '{"text": "updated"}');
    await expect.element(body).toHaveAttribute("aria-invalid", "false");
    await userEvent.fill(
      builder("Create").getByLabelText("Value of header 1"),
      "Bearer {{ index .Secrets $.x }}",
    );
    await page.getByRole("button", { name: "Save" }).click();
    await expect
      .element(page.getByTestId("destination-type-create-header-0-value-error-text"))
      .toHaveTextContent(
        "Line 1, column 11: Read a Secret as {{ .Secrets.<name> }} and an extracted value as {{ .Response.<name> }}: other uses of .Secrets and .Response are refused.",
      );
  });

  test("warns of a literal credential at its field while it holds the stored value", async () => {
    mockReads();
    await render(
      providers(
        <DestinationForm
          kind={WEBHOOK_KIND}
          destination={DESTINATION}
          submitLabel="Save"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    const value = page.getByLabelText("Value of header 1");
    await expect.element(value).toHaveAccessibleDescription(LITERAL);
    await expect.element(page.getByTestId("literal-credential")).toHaveTextContent(LITERAL);
    // Only the Authorization header is warned of; X-Team holds no credential.
    expect(page.getByTestId("literal-credential").elements()).toHaveLength(1);
    await userEvent.fill(value, "Bearer {{ .Secrets.token }}");
    await expect.element(page.getByTestId("literal-credential")).not.toBeInTheDocument();
    await userEvent.fill(value, "Bearer abc");
    await expect.element(page.getByTestId("literal-credential")).toBeVisible();
  });

  test("shows a Viewer the templates with their .Secrets references, read-only", async () => {
    mockReads();
    await render(
      providers(
        <DestinationForm
          kind={WEBHOOK_KIND}
          destination={TEMPLATE_DESTINATION}
          readOnly
          submitLabel="Save"
          save={() => Promise.resolve(undefined)}
        />,
        READ,
      ),
    );
    await expect
      .element(page.getByLabelText("Value of header 1"))
      .toHaveValue("Bearer {{ .Secrets.token }}");
    await expect.element(page.getByLabelText("Value of header 1")).toBeDisabled();
    await expect
      .element(page.getByRole("tab", { name: "Template" }))
      .toHaveAttribute("aria-selected", "true");
    await expect.element(page.getByRole("button", { name: "Add header" })).not.toBeInTheDocument();
    await expect.element(page.getByRole("button", { name: "Add rule" })).not.toBeInTheDocument();
    await expect.element(page.getByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });

  test("shows the fields in Russian", async () => {
    await i18n.changeLanguage("ru");
    mockReads();
    await render(
      providers(
        <DestinationForm
          kind={WEBHOOK_KIND}
          destination={DESTINATION}
          submitLabel="Сохранить"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    await expect
      .element(page.getByRole("tablist", { name: "Режим" }))
      .toHaveTextContent("СобытияШаблонОба режима");
    await expect
      .element(page.getByTestId("literal-credential"))
      .toHaveTextContent(
        "Храните учётные данные в секретах: это значение видят все, кто может читать места доставки.",
      );
    await page.getByRole("tab", { name: "Шаблон" }).click();
    await expect.element(builder("Ответ в треде")).toBeVisible();
    await expect.element(builder("Создание").getByText("Правила извлечения")).toBeVisible();
  });
});

describe("refusals, changes and versions", () => {
  test("names the extracted value a request may not read, a URL that is not absolute and a line break", () => {
    const t = i18n.t.bind(i18n);
    const v = webhookValues(TEMPLATE_DESTINATION);
    expect(
      responseRefs('{{ .Response.id }} {{ index $.Response "thread" }} {{ .Responses.x }}'),
    ).toEqual(["id", "thread"]);
    expect(unreadableValue(v, "/template/update/url", "{{ .Response.id }}")).toBeUndefined();
    expect(unreadableValue(v, "/template/update/url", "{{ .Response.thread }}")).toBe("thread");
    expect(unreadableValue(v, "/template/create/body", "{{ .Response.id }}")).toBe("id");
    expect(unreadableValue(v, "/events/url", "{{ .Response.id }}")).toBeUndefined();
    v.added.open_thread = true;
    v.requests.open_thread.extract = [{ name: "thread", path: "$.thread.id" }];
    expect(
      unreadableValue(v, "/template/reply_in_thread/url", "{{ .Response.thread }}"),
    ).toBeUndefined();
    expect(unreadableValue(v, "/template/update/url", "{{ .Response.thread }}")).toBe("thread");
    const invalid = { code: "invalid_format", detail: "server text" };
    expect(shownProblem(t, "/template/update/url", invalid, "thread").detail).toBe(
      "No extraction rule this request may read gives {{ .Response.thread }}: add it to “Create” (or to “Open thread” for “Reply in thread”).",
    );
    expect(shownProblem(t, "/events/url", invalid).detail).toBe(
      "Enter an absolute http or https URL; it must stay one once its templates are filled in.",
    );
    expect(shownProblem(t, "/events/headers/2/value", invalid).detail).toBe(
      "The header value must not contain a line break.",
    );
    expect(shownProblem(t, "/template/update/body", invalid).detail).toBe(
      "This value has the wrong format.",
    );
  });

  test("a change touches the fields whose values, request or mode it changes", () => {
    const before = webhookValues(TEMPLATE_DESTINATION);
    const after = webhookValues(TEMPLATE_DESTINATION);
    after.requests.create.url = "http://127.0.0.1:18093/chat/other";
    expect(webhookChanged(before, after, "/template/create/url")).toBe(true);
    expect(webhookChanged(before, after, "/template/update/body")).toBe(false);
    expect(webhookChanged(before, after, "/proxy/address")).toBe(false);
    const added = webhookValues(TEMPLATE_DESTINATION);
    added.added.open_thread = true;
    expect(webhookChanged(before, added, "/template/open_thread/url")).toBe(true);
    expect(webhookChanged(before, added, "/template/create/url")).toBe(false);
    const mode = webhookValues(TEMPLATE_DESTINATION);
    mode.mode = "both";
    expect(webhookChanged(before, mode, "/events")).toBe(true);
    const proxy = webhookValues(TEMPLATE_DESTINATION);
    proxy.proxy.enabled = true;
    expect(webhookChanged(before, proxy, "/proxy/address")).toBe(true);
  });

  test("a new version with the same settings is adopted, changes or not; another one is not", () => {
    const secretSet = { ...DESTINATION, etag: '"5"' };
    expect(versionAction(WEBHOOK_KIND, DESTINATION, DESTINATION, true)).toBe("same");
    expect(versionAction(WEBHOOK_KIND, secretSet, DESTINATION, true)).toBe("adopt");
    expect(versionAction(WEBHOOK_KIND, secretSet, DESTINATION, false)).toBe("adopt");
    const otherLimiter = { ...DESTINATION, etag: '"5"', limiter: { limit: 9, per_seconds: 1 } };
    expect(versionAction(WEBHOOK_KIND, otherLimiter, DESTINATION, true)).toBe("keep");
    expect(versionAction(WEBHOOK_KIND, otherLimiter, DESTINATION, false)).toBe("replace");
    // A proxy password changed elsewhere is no value of the form, but it is a change of the settings.
    const otherPassword: WebhookDestination = {
      ...DESTINATION,
      etag: '"5"',
      proxy: { enabled: false, password_status: { set: true, updated_at: "2026-10-09T12:00:00Z" } },
    };
    expect(versionAction(WEBHOOK_KIND, otherPassword, DESTINATION, true)).toBe("keep");
  });

  test("editing a field clears its problem only, and a problem of a whole list takes the focus", async () => {
    mockReads();
    const save = vi
      .fn((_input: DestinationInput) => Promise.resolve(undefined))
      .mockRejectedValueOnce(
        refused(
          { pointer: "/template/create/extract", code: "too_long", detail: "too many" },
          {
            pointer: "/template/update/url",
            code: "invalid_format",
            detail: "No extraction rule this request may read gives the value thread.",
          },
        ),
      );
    await render(
      providers(
        <DestinationForm
          kind={WEBHOOK_KIND}
          destination={TEMPLATE_DESTINATION}
          submitLabel="Save"
          save={save}
        />,
      ),
    );
    const updateUrl = builder("Update").getByLabelText("URL", { exact: true });
    await userEvent.fill(
      updateUrl,
      "http://127.0.0.1:18093/chat/ops/messages/{{ .Response.thread }}",
    );
    await page.getByRole("button", { name: "Save" }).click();
    const listError = page.getByTestId("destination-type-create-extract-error-text");
    await expect.element(listError).toHaveTextContent("This value is too long.");
    await expect.element(listError).toHaveFocus();
    await expect
      .element(page.getByTestId("destination-type-update-url-error-text"))
      .toHaveTextContent(
        "No extraction rule this request may read gives {{ .Response.thread }}: add it to “Create” (or to “Open thread” for “Reply in thread”).",
      );
    // Another request's field keeps its problem; the edited one loses it.
    await userEvent.fill(
      builder("Create").getByLabelText("URL", { exact: true }),
      "http://127.0.0.1:18093/x",
    );
    await expect.element(page.getByTestId("destination-type-update-url-error-text")).toBeVisible();
    await userEvent.fill(updateUrl, "http://127.0.0.1:18093/chat/ops/messages/{{ .Response.id }}");
    await expect
      .element(page.getByTestId("destination-type-update-url-error-text"))
      .not.toBeInTheDocument();
  });

  test("a removed header keeps the elements of the headers after it", async () => {
    mockReads();
    await render(
      providers(
        <DestinationForm
          kind={WEBHOOK_KIND}
          destination={DESTINATION}
          submitLabel="Save"
          save={() => Promise.resolve(undefined)}
        />,
      ),
    );
    const second = page.getByLabelText("Value of header 2").element();
    await page.getByRole("button", { name: "Remove header 1" }).click();
    await expect.element(page.getByLabelText("Value of header 1")).toHaveValue("ops");
    expect(page.getByLabelText("Value of header 1").element()).toBe(second);
  });
});

describe("Delete of an outgoing webhook", () => {
  test("says what happens in each mode", async () => {
    const t = i18n.t.bind(i18n);
    expect(deleteDescription(t, DESTINATION)).toContain(
      "Queued events will not be sent. The Signing secret and the Secrets are deleted now.",
    );
    expect(deleteDescription(t, TEMPLATE_DESTINATION)).toContain(
      "Open messages get a last update, then the secrets are deleted.",
    );
    const both = deleteDescription(t, { ...DESTINATION, mode: "both" });
    expect(both).toContain("Queued events will not be sent.");
    expect(both).toContain("Open messages get a last update, then the secrets are deleted.");

    vi.spyOn(window, "fetch").mockResolvedValue(new Response(null, { status: 204 }));
    const rootRoute = createRootRoute({
      component: () => <DestinationDeleteDialog destination={DESTINATION} />,
    });
    const router = createRouter({
      routeTree: rootRoute,
      history: createMemoryHistory({ initialEntries: ["/destinations/DS0000000000WH"] }),
    });
    await render(providers(<RouterProvider router={router} />));
    await page.getByRole("button", { name: "Delete" }).click();
    await expect
      .element(page.getByTestId("destination-delete-description"))
      .toHaveTextContent(
        "Muster stops sending here, and the Destination leaves every Route. Queued events will not be sent. The Signing secret and the Secrets are deleted now. This cannot be undone.",
      );
  });
});
