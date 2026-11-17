// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The warnings and "On call now" of a Schedule in a real browser (C-27.FR-5, FR-7, FR-8, C-27.AC-6, AC-9): "Gap: {from}
// – {to}", "Disabled: @{login} in {Rotation layer}", "@{login} is a Viewer", "@{login} is not in the Team", each
// reason an On-call is unreachable, and "On call now" with the person, the Rotation layer, until when, the marks and
// what comes next — in English and in Russian.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { DestinationRef, ScheduleWarning } from "../api/gen/model";
import i18n from "../i18n";
import { OLGA, PETR, SERGEY, onCall, segment, warning, hasText } from "../test/fixtures/schedules";
import { ScheduleOnCallNow } from "./schedule-on-call-now";
import { ScheduleWarnings, distinctWarnings } from "./schedule-warnings";

const ZONE = "Europe/Berlin";
const LAYER = { id: "RL0000000000AA", name: "Around the clock" };

function destination(name: string, type: DestinationRef["type"] = "mattermost"): DestinationRef {
  return { id: `DS${name}`, name, type, health: { state: "healthy" } };
}

function reach(
  name: string,
  type: DestinationRef["type"],
  reason: ScheduleWarning["reason"],
): ScheduleWarning {
  return warning({
    kind: "unreachable",
    user: PETR,
    rotation_layer: LAYER,
    start: "2026-11-16T09:00:00Z",
    end: "2026-11-17T09:00:00Z",
    destination: destination(name, type),
    messenger: type,
    reason,
  });
}

const GAP = warning({
  kind: "gap",
  start: "2026-11-23T18:00:00Z",
  end: "2026-11-24T10:00:00Z",
});
const DISABLED = warning({ kind: "disabled", user: SERGEY, rotation_layer: LAYER });

async function renderWarnings(warnings: ScheduleWarning[]) {
  await render(
    <I18nextProvider i18n={i18n}>
      <ScheduleWarnings warnings={warnings} zone={ZONE} />
    </I18nextProvider>,
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("Warnings", () => {
  test("lists a gap and a disabled person", async () => {
    await renderWarnings([DISABLED, GAP]);
    const list = page.getByTestId("schedule-warnings");
    await hasText(list, "Disabled: @sergey in Around the clock");
    await hasText(list, "Gap: Mon 19:00 – Tue 11:00");
  });

  test("names a Viewer and a person who is not in the Team", async () => {
    await renderWarnings([
      warning({ kind: "viewer", user: OLGA, rotation_layer: LAYER }),
      warning({ kind: "not_in_team", user: PETR, rotation_layer: LAYER }),
    ]);
    const list = page.getByTestId("schedule-warnings");
    await hasText(list, "@olga is a Viewer");
    await hasText(list, "@petr is not in the Team");
  });

  test("names a disabled person of an Override, which has no Rotation layer", async () => {
    await renderWarnings([
      warning({ kind: "disabled", user: SERGEY, override_id: "OV0000000000AA" }),
    ]);
    await hasText(page.getByTestId("schedule-warnings"), "Disabled: @sergey in an Override");
  });

  test("says every reason an On-call is unreachable, and says it once for the segments of a person", async () => {
    await renderWarnings([
      reach("payments-notice", "mattermost", "no_account_link"),
      {
        ...reach("payments-notice", "mattermost", "no_account_link"),
        start: "2026-11-18T09:00:00Z",
      },
      reach("payments-bridge", "bridge", "bridge_no_native_mentions"),
      reach("payments-hook", "webhook", "webhook_no_thread_reply"),
      reach("payments-tg", "telegram", "not_in_discussion_group"),
    ]);
    const list = page.getByTestId("schedule-warnings");
    await hasText(list, "@petr: Unreachable in Mattermost: no linked account.");
    await hasText(list, "Unreachable in payments-bridge: the bridge cannot mention people.");
    await hasText(
      list,
      "Unreachable in payments-hook: the outgoing webhook has no reply in thread.",
    );
    await hasText(list, "Unreachable in payments-tg: not in the discussion group.");
    expect(list.element().querySelectorAll("li")).toHaveLength(4);
  });

  test("follows an unreachable mark of a person a gateway reaches with the call that works", async () => {
    await renderWarnings([
      { ...reach("payments-notice", "mattermost", "no_account_link"), gateway_reaches: true },
      {
        ...reach("payments-hook", "webhook", "webhook_no_thread_reply"),
        user: OLGA,
        gateway_reaches: false,
      },
    ]);
    const lines = page.getByTestId("schedule-warnings").element().querySelectorAll("li");
    expect(lines[0]?.textContent).toContain(
      "Unreachable in Mattermost: no linked account. A call through the Notification gateway works.",
    );
    expect(lines[1]?.textContent).not.toContain("A call through the Notification gateway works.");
    await i18n.changeLanguage("ru");
    await hasText(page.getByTestId("schedule-warnings"), "Звонок через шлюз оповещений доходит.");
  });

  test("says there are none", async () => {
    await renderWarnings([]);
    await hasText(page.getByTestId("schedule-no-warnings"), "No warnings.");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderWarnings([DISABLED, GAP]);
    await expect.element(page.getByRole("heading", { name: "Предупреждения" })).toBeVisible();
    const list = page.getByTestId("schedule-warnings");
    await hasText(list, "Отключён: @sergey в слое «Around the clock»");
    await hasText(list, "Дыра: пн 19:00 – вт 11:00");
  });

  test("keeps one line of a warning that came twice", () => {
    expect(distinctWarnings([DISABLED, DISABLED, GAP, GAP])).toHaveLength(2);
  });
});

describe("On call now", () => {
  const FROM = "2026-11-16T09:30:00Z";
  const MATTERMOST = warning({
    kind: "unreachable",
    user: PETR,
    rotation_layer: LAYER,
    start: FROM,
    end: "2026-11-16T10:00:00Z",
    destination: destination("payments-notice"),
    messenger: "mattermost",
    reason: "no_account_link",
  });

  async function renderNow(overrides: Parameters<typeof onCall>[0]) {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>
          <ScheduleOnCallNow
            data={onCall({ from: FROM, to: "2026-11-30T09:30:00Z", ...overrides })}
            zone={ZONE}
          />
        </QueryClientProvider>
      </I18nextProvider>,
    );
  }

  test("shows the person, the Rotation layer and until when, the mark and the next person", async () => {
    await renderNow({
      segments: [
        segment(FROM, "2026-11-16T10:00:00Z", PETR),
        segment("2026-11-16T10:00:00Z", "2026-11-23T09:00:00Z", OLGA, {
          id: "RL0000000000BB",
          name: "Working hours 11-19",
        }),
      ],
      warnings: [MATTERMOST],
    });
    const person = page.getByTestId("schedule-on-call-person");
    await hasText(person, "@petr");
    await hasText(person, "Around the clock");
    await hasText(person, "until 11:00");
    await hasText(
      page.getByTestId("schedule-unreachable"),
      "Unreachable in Mattermost: no linked account.",
    );
    await hasText(page.getByTestId("schedule-next"), "Next: @olga from 11:00");
  });

  test("follows the unreachable mark with the call that works when a gateway reaches the On-call", async () => {
    await renderNow({
      segments: [segment(FROM, "2026-11-16T18:00:00Z", PETR)],
      warnings: [{ ...MATTERMOST, gateway_reaches: true }],
    });
    const marks = page.getByTestId("schedule-unreachable");
    await hasText(
      marks,
      "Unreachable in Mattermost: no linked account. A call through the Notification gateway works.",
    );
    await i18n.changeLanguage("ru");
    await hasText(marks, "Звонок через шлюз оповещений доходит.");
  });

  test("adds nothing to the unreachable mark without gateway_reaches", async () => {
    await renderNow({
      segments: [segment(FROM, "2026-11-16T18:00:00Z", PETR)],
      warnings: [{ ...MATTERMOST, gateway_reaches: false }],
    });
    await hasText(page.getByTestId("schedule-unreachable"), "no linked account.");
    expect(page.getByTestId("schedule-gateway-reaches").elements()).toHaveLength(0);
  });

  test("shows no mark for an On-call a Destination reaches, and names a gap that follows", async () => {
    await renderNow({
      segments: [
        segment(FROM, "2026-11-16T18:00:00Z", PETR),
        segment("2026-11-16T18:00:00Z", "2026-11-17T10:00:00Z", null),
      ],
    });
    await expect.element(page.getByTestId("schedule-unreachable")).not.toBeInTheDocument();
    await hasText(page.getByTestId("schedule-next"), "Next: gap from 19:00");
  });

  test("says nobody is On-call in a gap", async () => {
    await renderNow({ segments: [segment(FROM, "2026-11-16T18:00:00Z", null)] });
    await hasText(page.getByTestId("schedule-on-call-person"), "Nobody is On-call. until 19:00");
  });

  test("says an Override instead of a Rotation layer", async () => {
    await renderNow({
      segments: [
        {
          start: FROM,
          end: "2026-11-16T18:00:00Z",
          user: PETR,
          source: { kind: "override", override_id: "OV0000000000AA" },
        },
      ],
    });
    await hasText(page.getByTestId("schedule-on-call-person"), "Override");
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    await renderNow({
      segments: [
        segment(FROM, "2026-11-16T10:00:00Z", PETR),
        segment("2026-11-16T10:00:00Z", "2026-11-17T10:00:00Z", null),
      ],
      warnings: [MATTERMOST],
    });
    await expect.element(page.getByRole("heading", { name: "Сейчас дежурит" })).toBeVisible();
    await hasText(page.getByTestId("schedule-on-call-person"), "до 11:00");
    await hasText(
      page.getByTestId("schedule-unreachable"),
      "Недостижим в Mattermost: нет привязанной учётной записи.",
    );
    await hasText(page.getByTestId("schedule-next"), "Дальше: дыра с 11:00");
  });
});
