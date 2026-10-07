// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The Heartbeat badge and banners in a real browser: each of the four states has its own text and icon, in English and
// Russian; the banners show the texts of the Heartbeat warnings, the time of a lost Heartbeat as HH:MM in the time
// zone with the date in front when it is not today; and the other warnings show no Heartbeat banner.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { page } from "vitest/browser";
import { render } from "vitest-browser-react";

import type { HeartbeatState, IntegrationWarning } from "../api/gen/model";
import i18n from "../i18n";
import { SESSION_QUERY_KEY, type SessionRead } from "../lib/api";
import { browserTimeZone, formatTime } from "../lib/time";
import { HeartbeatBadge } from "./heartbeat-badge";
import { HeartbeatBanner, lostSinceText } from "./integration-warnings";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

async function renderIn(children: ReactNode) {
  const queryClient = new QueryClient();
  const read: SessionRead = { session: null, ended: false };
  queryClient.setQueryData(SESSION_QUERY_KEY, read);
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>,
  );
}

const STATES: [HeartbeatState, string, string][] = [
  ["not_configured", "Not configured", "Не настроен"],
  ["waiting", "Waiting", "Ожидание"],
  ["live", "Live", "На связи"],
  ["lost", "Lost", "Нет связи"],
];

describe("HeartbeatBadge", () => {
  test.each(STATES)("shows %s as text with an icon", async (state, english, russian) => {
    const screen = await renderIn(<HeartbeatBadge state={state} />);
    const badge = screen.getByTestId("heartbeat-badge");
    await expect.element(badge).toHaveTextContent(english, { normalizeWhitespace: true });
    await expect.element(badge).toHaveAttribute("data-state", state);
    expect(badge.element().querySelector("svg[aria-hidden='true']")).not.toBeNull();
    await i18n.changeLanguage("ru");
    await expect.element(badge).toHaveTextContent(russian);
  });

  test("the four states look different", async () => {
    const screen = await renderIn(
      <>
        {STATES.map(([state]) => (
          <HeartbeatBadge key={state} state={state} />
        ))}
      </>,
    );
    const looks = screen
      .getByTestId("heartbeat-badge")
      .elements()
      .map((el) => `${el.className}|${el.querySelector("svg")?.getAttribute("class") ?? ""}`);
    expect(new Set(looks).size).toBe(4);
  });

  test("labelled names the Heartbeat for screen readers only", async () => {
    const screen = await renderIn(<HeartbeatBadge state="lost" labelled />);
    const badge = screen.getByTestId("heartbeat-badge");
    await expect.element(badge).toHaveTextContent("Heartbeat: Lost");
    await expect.element(page.getByText("Heartbeat:")).toHaveClass("sr-only");
  });
});

describe("HeartbeatBanner", () => {
  test("without a Heartbeat", async () => {
    const screen = await renderIn(
      <HeartbeatBanner warnings={[{ kind: "heartbeat_not_configured" }]} />,
    );
    await expect
      .element(screen.getByTestId("heartbeat-banner"))
      .toHaveTextContent(
        "No Heartbeat: Muster will not notice when Alertmanager goes quiet, and Stale resolution is off.",
      );
  });

  test("before the first signal", async () => {
    const screen = await renderIn(<HeartbeatBanner warnings={[{ kind: "heartbeat_waiting" }]} />);
    await expect
      .element(screen.getByTestId("heartbeat-banner"))
      .toHaveTextContent(
        "Waiting for the first Heartbeat signal. Stale resolution is off until it arrives.",
      );
  });

  test("lost, with the time of the last signal in the time zone", async () => {
    const since = new Date(Date.now() - 60_000).toISOString();
    const warnings: IntegrationWarning[] = [
      { kind: "snapshot_truncated", truncated_group_count: 1 },
      { kind: "heartbeat_lost", since },
    ];
    const screen = await renderIn(<HeartbeatBanner warnings={warnings} />);
    const banner = screen.getByTestId("heartbeat-banner");
    const time = lostSinceText(since, browserTimeZone(), "en");
    await expect
      .element(banner)
      .toHaveTextContent(
        `No contact with Alertmanager since ${time}. Nothing is resolved as Stale until contact returns.`,
      );
    await expect.element(banner).toHaveAttribute("data-kind", "heartbeat_lost");
    await i18n.changeLanguage("ru");
    await expect
      .element(banner)
      .toHaveTextContent(
        `Связи с Alertmanager нет с ${lostSinceText(since, browserTimeZone(), "ru")}. Пока она не восстановится, ничего не закрывается как устаревшее.`,
      );
  });

  test("the other warnings show no Heartbeat banner", async () => {
    const screen = await renderIn(
      <HeartbeatBanner
        warnings={[
          { kind: "long_repeat_interval", route_path: "{}", repeat_interval_seconds: 3600 },
        ]}
      />,
    );
    expect(screen.getByTestId("heartbeat-banner").elements()).toHaveLength(0);
  });
});

describe("lostSinceText", () => {
  const now = new Date("2026-10-07T12:00:00Z");

  test("today is HH:MM in the time zone", () => {
    expect(lostSinceText("2026-10-07T09:05:00Z", "Europe/Berlin", "en", now)).toBe("11:05");
    expect(lostSinceText("2026-10-07T09:05:00Z", "Asia/Tokyo", "en", now)).toBe(
      formatTime("2026-10-07T09:05:00Z", "Asia/Tokyo", "en"),
    );
  });

  test("another day has the date in front", () => {
    expect(lostSinceText("2026-10-06T21:30:00Z", "Europe/Berlin", "en", now)).toBe("Oct 6, 23:30");
    expect(lostSinceText("2026-10-06T21:30:00Z", "Europe/Berlin", "ru", now)).toBe("6 окт., 23:30");
  });

  test("another year has the year too", () => {
    expect(lostSinceText("2025-12-31T10:00:00Z", "Europe/Berlin", "en", now)).toBe(
      "Dec 31, 2025, 11:00",
    );
  });

  test("today is decided in the time zone, not in UTC", () => {
    // 22:30 UTC on the 6th is already the 7th in Moscow.
    expect(lostSinceText("2026-10-06T22:30:00Z", "Europe/Moscow", "en", now)).toBe("01:30");
  });
});
