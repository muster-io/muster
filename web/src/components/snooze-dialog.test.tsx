// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Snooze dialog in a real browser: the Route's durations as quick choices and none in a bulk Snooze; "No end"
// shows its warning and keeps "Snooze" disabled until "I understand" is ticked, then sends no_end; "Until" takes a
// date and a time in the user's time zone, sends them in UTC and refuses an end in the past; and the Russian texts.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { Session, SnoozeRequest } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { snoozeDurationLabel } from "../lib/commands";
// oxlint-disable-next-line import/no-unassigned-import -- the stylesheet is imported for its side effect
import "../styles.css";
import { SnoozeDialog, dateTimeParts, instantOf } from "./snooze-dialog";

const ZONE = "Europe/Berlin";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

async function renderDialog(props: { durations?: number[]; bulk?: boolean } = {}) {
  const queryClient = new QueryClient();
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
        <SnoozeDialog
          open
          onOpenChange={() => {}}
          title="Snooze #412"
          durations={props.durations}
          bulk={props.bulk}
          pending={false}
          onSnooze={(r) => sent.push(r)}
        />
      </QueryClientProvider>
    </I18nextProvider>,
  );
  return { sent, dialog: page.getByRole("dialog", { name: "Snooze #412" }) };
}

const radios = () =>
  page
    .getByRole("radio")
    .elements()
    .map((e) => e.closest("label")?.textContent);

describe("SnoozeDialog", () => {
  test("offers the Route's durations as quick choices, then Until and No end", async () => {
    const { dialog, sent } = await renderDialog({ durations: [14400, 3600, 86400] });
    await expect.element(dialog.getByRole("radio", { name: "1 h" })).toBeChecked();
    expect(radios()).toEqual(["1 h", "4 h", "24 h", "Until", "No end"]);
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

  test("a bulk Snooze offers Until and No end only, with the warning for all of them", async () => {
    const { dialog } = await renderDialog({ bulk: true });
    expect(radios()).toEqual(["Until", "No end"]);
    await expect.element(dialog.getByRole("radio", { name: "Until" })).toBeChecked();
    await dialog.getByText("No end", { exact: true }).click();
    await expect
      .element(dialog.getByTestId("snooze-no-end-warning"))
      .toHaveTextContent("These Alert Groups stay snoozed until someone unsnoozes them.");
  });

  test("Until takes a date and a time in the user's time zone and sends UTC", async () => {
    const { dialog, sent } = await renderDialog();
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
    await dialog.getByLabelText("Date").fill("2020-01-01");
    await expect.element(dialog.getByText("The Snooze end must be in the future.")).toBeVisible();
    await expect.element(dialog.getByRole("button", { name: "Snooze" })).toBeDisabled();
    await expect.element(dialog.getByLabelText("Date")).toHaveAttribute("aria-invalid", "true");
  });

  test("speaks Russian", async () => {
    await i18n.changeLanguage("ru");
    const { dialog } = await renderDialog({ durations: [3600, 172800] });
    expect(radios()).toEqual(["1 ч", "2 д", "До даты", "Без срока"]);
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
