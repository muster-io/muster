// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Snooze dialog in a real browser, against a stubbed API: the Route's durations as quick choices and none in a
// bulk Snooze; the Snooze presets after them, labelled from the Organization's settings, each with the end that
// listSnoozePresets answers for the user's time zone, read afresh on every open, sent as a preset with that time zone,
// offered without their ends while the ends are read or cannot be; "No end" shows its warning and keeps "Snooze" disabled until "I understand" is
// ticked, then sends no_end; "Until" takes a date and a time in the user's time zone, sends them in UTC and refuses an
// end in the past; and the Russian texts.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Session, SnoozePresetList, SnoozeRequest } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { snoozeDurationLabel } from "../lib/commands";
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { SnoozeDialog, dateTimeParts, instantOf } from "./snooze-dialog";

const ZONE = "Europe/Berlin";

/** The ends at 02:00 on Tuesday 20 October 2026 in Berlin (C-10.AC-21). */
const ENDS: SnoozePresetList = {
  time_zone: ZONE,
  workday_start: "09:00",
  week_start: "monday",
  items: [
    { preset: "tomorrow", until: "2026-10-20T07:00:00Z" },
    { preset: "week_start", until: "2026-10-26T08:00:00Z" },
  ],
};

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status < 300 ? "application/json" : "application/problem+json" },
  });
}

const failure = () => json(500, { code: "internal", title: "Internal error" });

/** The answers of the stubbed API; a function that returns undefined leaves the read pending. */
let organization: () => Response | undefined;
let presets: () => Response | undefined;
/** The reads of listSnoozePresets, with their query. */
let presetReads: string[] = [];

beforeEach(async () => {
  await i18n.changeLanguage("en");
  presetReads = [];
  // The dialog reads only these settings of the Organization.
  organization = () =>
    json(200, { time_zone: "UTC", workday_start: "09:00", week_start: "monday" });
  presets = () => json(200, ENDS);
  vi.spyOn(window, "fetch").mockImplementation((input) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
      window.location.origin,
    );
    let answer: Response | undefined;
    if (url.pathname === "/api/v1/organization") {
      answer = organization();
    } else if (url.pathname === "/api/v1/snooze-presets") {
      presetReads.push(url.search);
      answer = presets();
    } else {
      answer = json(404, { code: "not_found" });
    }
    return answer === undefined ? new Promise<Response>(() => {}) : Promise.resolve(answer);
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

/** The dialog as a page opens it: open at first, closed by its own buttons, and opened again by "Open". */
function Reopenable({
  durations,
  onSnooze,
}: {
  durations?: number[];
  onSnooze: (request: SnoozeRequest) => void;
}) {
  const [open, setOpen] = useState(true);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open
      </button>
      <SnoozeDialog
        open={open}
        onOpenChange={setOpen}
        title="Snooze #412"
        durations={durations}
        pending={false}
        onSnooze={onSnooze}
      />
    </>
  );
}

async function renderDialog(
  props: { durations?: number[]; bulk?: boolean; reopenable?: boolean } = {},
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: SessionRead = {
    session: {
      state: "active",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the dialog reads only the time zone
      user: { id: "US0000000000ME", time_zone: ZONE } as Session["user"],
      csrf_token: "csrf",
      expires_at: "2026-10-08T09:00:00Z",
      idle_expires_at: "2026-10-08T09:00:00Z",
      method: "local",
      permissions: [],
    },
    ended: false,
  };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  const sent: SnoozeRequest[] = [];
  await render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        {props.reopenable === true ? (
          <Reopenable durations={props.durations} onSnooze={(r) => sent.push(r)} />
        ) : (
          <SnoozeDialog
            open
            onOpenChange={() => {}}
            title="Snooze #412"
            durations={props.durations}
            bulk={props.bulk}
            pending={false}
            onSnooze={(r) => sent.push(r)}
          />
        )}
      </QueryClientProvider>
    </I18nextProvider>,
  );
  const dialog = page.getByRole("dialog", { name: "Snooze #412" });
  // The form shows once the Organization's settings are read.
  await expect.element(dialog.getByRole("radio").last()).toBeInTheDocument();
  return { sent, dialog };
}

/** The choices of the dialog by the names of their radios. */
const radios = () =>
  page
    .getByRole("radio")
    .elements()
    .map((e) => document.getElementById(e.getAttribute("aria-labelledby") ?? "")?.textContent);

describe("SnoozeDialog", () => {
  test("offers the Route's durations as quick choices, then the presets, Until and No end", async () => {
    const { dialog, sent } = await renderDialog({ durations: [14400, 3600, 86400] });
    await expect.element(dialog.getByRole("radio", { name: "1 h" })).toBeChecked();
    expect(radios()).toEqual([
      "1 h",
      "4 h",
      "24 h",
      "Until tomorrow 09:00",
      "Until Monday 09:00",
      "Until",
      "No end",
    ]);
    const before = Date.now();
    await dialog.getByRole("button", { name: "Snooze" }).click();
    expect(sent).toHaveLength(1);
    const until = Date.parse(sent[0]?.until ?? "");
    expect(until - before).toBeGreaterThanOrEqual(3600 * 1000 - 1000);
    expect(until - before).toBeLessThan(3600 * 1000 + 60_000);
  });

  test("No end shows its warning and needs I understand before Snooze", async () => {
    const { dialog, sent } = await renderDialog({ durations: [3600] });
    await dialog.getByText("No end", { exact: true }).click();
    await expect
      .element(dialog.getByTestId("snooze-no-end-warning"))
      .toHaveTextContent("This Alert Group stays snoozed until someone unsnoozes it.");
    const snooze = dialog.getByRole("button", { name: "Snooze" });
    await expect.element(snooze).toBeDisabled();
    await dialog.getByLabelText("I understand").click();
    await expect.element(snooze).toBeEnabled();
    await snooze.click();
    expect(sent).toEqual([{ no_end: true }]);
  });

  test("offers the presets with the ends the server computed for the user's time zone, and sends one", async () => {
    const { dialog, sent } = await renderDialog({ durations: [3600] });
    const tomorrow = dialog.getByRole("radio", { name: "Until tomorrow 09:00", exact: true });
    await expect
      .element(dialog.getByTestId("snooze-preset-end-tomorrow"))
      .toHaveTextContent("Ends Oct 20, 2026, 09:00");
    await expect
      .element(dialog.getByTestId("snooze-preset-end-week_start"))
      .toHaveTextContent("Ends Oct 26, 2026, 09:00");
    await expect.element(tomorrow).toHaveAccessibleDescription("Ends Oct 20, 2026, 09:00");
    expect(presetReads).toEqual([`?time_zone=${encodeURIComponent(ZONE)}`]);
    await dialog.getByTestId("snooze-choice-tomorrow").click();
    await expect.element(tomorrow).toBeChecked();
    await dialog.getByRole("button", { name: "Snooze" }).click();
    expect(sent).toEqual([{ preset: "tomorrow", time_zone: ZONE }]);
  });

  test("reads the ends afresh on every open and never shows those of an earlier open", async () => {
    const { dialog } = await renderDialog({ durations: [3600], reopenable: true });
    await expect
      .element(dialog.getByTestId("snooze-preset-end-tomorrow"))
      .toHaveTextContent("Ends Oct 20, 2026, 09:00");
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect.element(dialog).not.toBeInTheDocument();
    presets = () => undefined;
    await page.getByRole("button", { name: "Open", exact: true }).click();
    await expect
      .element(dialog.getByRole("radio", { name: "Until tomorrow 09:00", exact: true }))
      .toBeInTheDocument();
    const read = `?time_zone=${encodeURIComponent(ZONE)}`;
    await vi.waitFor(() => expect(presetReads).toEqual([read, read]));
    await expect.element(dialog.getByTestId("snooze-preset-end-tomorrow")).not.toBeInTheDocument();
  });

  test("labels the presets from the Organization's settings while the ends are read", async () => {
    organization = () =>
      json(200, { time_zone: "UTC", workday_start: "08:30", week_start: "sunday" });
    presets = () => undefined;
    const { dialog } = await renderDialog({ durations: [3600] });
    expect(radios()).toEqual([
      "1 h",
      "Until tomorrow 08:30",
      "Until Sunday 08:30",
      "Until",
      "No end",
    ]);
    await expect.element(dialog.getByTestId("snooze-preset-end-tomorrow")).not.toBeInTheDocument();
  });

  test("labels the presets from the settings the ends were computed with", async () => {
    presets = () => json(200, { ...ENDS, workday_start: "08:30", week_start: "sunday" });
    const { dialog } = await renderDialog();
    await expect
      .element(dialog.getByRole("radio", { name: "Until Sunday 08:30", exact: true }))
      .toBeInTheDocument();
    expect(radios()).toEqual(["Until tomorrow 08:30", "Until Sunday 08:30", "Until", "No end"]);
  });

  test("a Route without durations offers the presets first, and the first is chosen", async () => {
    const { dialog, sent } = await renderDialog({ durations: [] });
    expect(radios()).toEqual(["Until tomorrow 09:00", "Until Monday 09:00", "Until", "No end"]);
    await expect
      .element(dialog.getByRole("radio", { name: "Until tomorrow 09:00", exact: true }))
      .toBeChecked();
    await dialog.getByRole("button", { name: "Snooze" }).click();
    expect(sent).toEqual([{ preset: "tomorrow", time_zone: ZONE }]);
  });

  test("while the ends are read, the presets show without them and can be chosen", async () => {
    presets = () => undefined;
    const { dialog, sent } = await renderDialog({ durations: [3600] });
    expect(radios()).toEqual([
      "1 h",
      "Until tomorrow 09:00",
      "Until Monday 09:00",
      "Until",
      "No end",
    ]);
    await expect.element(dialog.getByTestId("snooze-preset-end-tomorrow")).not.toBeInTheDocument();
    await dialog.getByTestId("snooze-choice-week_start").click();
    await dialog.getByRole("button", { name: "Snooze" }).click();
    expect(sent).toEqual([{ preset: "week_start", time_zone: ZONE }]);
  });

  test("when the ends cannot be read, the presets show without them and can be chosen", async () => {
    presets = failure;
    const { dialog, sent } = await renderDialog({ durations: [3600] });
    await vi.waitFor(() => expect(presetReads).toHaveLength(1));
    await expect
      .element(dialog.getByRole("radio", { name: "Until Monday 09:00", exact: true }))
      .toBeInTheDocument();
    await expect
      .element(dialog.getByTestId("snooze-preset-end-week_start"))
      .not.toBeInTheDocument();
    await dialog.getByTestId("snooze-choice-week_start").click();
    await dialog.getByRole("button", { name: "Snooze" }).click();
    expect(sent).toEqual([{ preset: "week_start", time_zone: ZONE }]);
  });

  test("without the Organization's settings and the ends, the presets are left out", async () => {
    organization = failure;
    presets = failure;
    await renderDialog({ durations: [3600] });
    await vi.waitFor(() => expect(presetReads).toHaveLength(1));
    expect(radios()).toEqual(["1 h", "Until", "No end"]);
  });

  test("a bulk Snooze offers the presets, Until and No end, with the warning for all of them", async () => {
    const { dialog } = await renderDialog({ bulk: true });
    expect(radios()).toEqual(["Until tomorrow 09:00", "Until Monday 09:00", "Until", "No end"]);
    await expect
      .element(dialog.getByRole("radio", { name: "Until tomorrow 09:00", exact: true }))
      .toBeChecked();
    await dialog.getByText("No end", { exact: true }).click();
    await expect
      .element(dialog.getByTestId("snooze-no-end-warning"))
      .toHaveTextContent("These Alert Groups stay snoozed until someone unsnoozes them.");
  });

  test("Until takes a date and a time in the user's time zone and sends UTC", async () => {
    const { dialog, sent } = await renderDialog();
    await dialog.getByText("Until", { exact: true }).click();
    await expect.element(dialog.getByText(`Time zone: ${ZONE}`)).toBeVisible();
    const next = new Date(Date.now() + 3 * 24 * 3600 * 1000);
    const day = dateTimeParts(next, ZONE).date;
    await dialog.getByLabelText("Date").fill(day);
    await dialog.getByLabelText("Time").fill("09:00");
    await dialog.getByRole("button", { name: "Snooze" }).click();
    expect(sent).toEqual([{ until: instantOf(day, "09:00", ZONE)?.toISOString() }]);
    expect(dateTimeParts(new Date(sent[0]?.until ?? ""), ZONE)).toEqual({
      date: day,
      time: "09:00",
    });
  });

  test("an end in the past keeps Snooze disabled and says why", async () => {
    const { dialog } = await renderDialog();
    await dialog.getByText("Until", { exact: true }).click();
    await dialog.getByLabelText("Date").fill("2020-01-01");
    await expect.element(dialog.getByText("The Snooze end must be in the future.")).toBeVisible();
    await expect.element(dialog.getByRole("button", { name: "Snooze" })).toBeDisabled();
    await expect.element(dialog.getByLabelText("Date")).toHaveAttribute("aria-invalid", "true");
  });

  test("speaks Russian", async () => {
    await i18n.changeLanguage("ru");
    const { dialog } = await renderDialog({ durations: [3600, 172800] });
    expect(radios()).toEqual([
      "1 ч",
      "2 д",
      "До завтра 09:00",
      "До понедельника 09:00",
      "До даты",
      "Без срока",
    ]);
    await expect
      .element(dialog.getByTestId("snooze-preset-end-tomorrow"))
      .toHaveTextContent("Закончится 20 окт. 2026 г., 09:00");
    await expect
      .element(dialog.getByTestId("snooze-preset-end-week_start"))
      .toHaveTextContent("Закончится 26 окт. 2026 г., 09:00");
    await dialog.getByText("Без срока", { exact: true }).click();
    await expect
      .element(dialog.getByTestId("snooze-no-end-warning"))
      .toHaveTextContent(
        "Эта группа алертов останется отложенной, пока кто-нибудь не снимет откладывание.",
      );
    await expect.element(dialog.getByLabelText("Я понимаю")).not.toBeChecked();
  });
});

describe("the times of the dialog", () => {
  test("a date and a time name an instant in the time zone, across a change of offset", () => {
    expect(instantOf("2026-10-07", "09:00", ZONE)?.toISOString()).toBe("2026-10-07T07:00:00.000Z");
    expect(instantOf("2026-12-07", "09:00", ZONE)?.toISOString()).toBe("2026-12-07T08:00:00.000Z");
    expect(instantOf("2026-10-07", "", ZONE)).toBeUndefined();
    expect(dateTimeParts(new Date("2026-10-07T22:30:00Z"), ZONE)).toEqual({
      date: "2026-10-08",
      time: "00:30",
    });
  });

  test("durations read as the quick choices show them", () => {
    const t = i18n.t;
    expect([1800, 3600, 14400, 86400, 259200, 90000].map((s) => snoozeDurationLabel(t, s))).toEqual(
      ["30 min", "1 h", "4 h", "24 h", "3 d", "25 h"],
    );
  });
});
