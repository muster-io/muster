// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A Notification rule in words (C-31.FR-5, C-31.AC-6): the default rules, "Call" as a Mention with a call at the same
// time, a wait of 5 minutes and a Mention, three Mentions, a call whose contact or gateway is gone, and the plural
// forms of the minutes in English and Russian.

import { afterEach, beforeEach, describe, expect, test } from "vitest";

import type { GatewayContact, NotificationRuleAction } from "../api/gen/model";
import i18n from "../i18n";
import { callGatewayId, confirmedContact, ruleWords } from "./notification-rules";

const mention: NotificationRuleAction = { kind: "mention" };
const wait = (minutes: number): NotificationRuleAction => ({
  kind: "wait",
  wait_seconds: minutes * 60,
});
const call = (extra: Partial<NotificationRuleAction> = {}): NotificationRuleAction => ({
  kind: "call",
  gateway_id: "CN00000000GW01",
  gateway: { id: "CN00000000GW01", name: "Example Gateway" },
  available: true,
  unavailable_reason: null,
  ...extra,
});
const contact = (overrides: Partial<GatewayContact> = {}): GatewayContact => ({
  id: "GC00000000AA01",
  connection: { id: "CN00000000GW01", name: "Example Gateway" },
  contact: "+10000000002",
  confirmed: true,
  confirmed_at: "2026-11-17T10:01:00Z",
  replaces: null,
  created_at: "2026-11-17T10:00:00Z",
  ...overrides,
});

const words = (rule: NotificationRuleAction[], contacts: GatewayContact[] | null = []) =>
  ruleWords(i18n.t, rule, contacts);

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("the default rules", () => {
  test("the normal rule is a Mention in the Thread of the Root message", () => {
    expect(words([mention])).toBe("a Mention in the Thread of the Root message");
  });

  test("the important rule is a Mention and again after 5 minutes if you did not answer", () => {
    expect(words([mention, wait(5), mention])).toBe(
      "a Mention in the Thread and again after 5 minutes if you did not answer",
    );
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    expect(words([mention])).toBe("упоминание в треде корневого сообщения");
    expect(words([mention, wait(5), mention])).toBe(
      "упоминание в треде и ещё раз через 5 минут, если вы не ответили",
    );
  });
});

describe("Call", () => {
  const important = [mention, call(), wait(5), mention];

  test("is a Mention and a call to the contact at the same time, again after 5 minutes", () => {
    expect(words(important, [contact()])).toBe(
      "a Mention in the Thread and a call to +10000000002 at the same time, again after 5 minutes",
    );
  });

  test("puts the call first in the same way", () => {
    expect(words([call(), mention, wait(5), mention], [contact()])).toBe(
      "a Mention in the Thread and a call to +10000000002 at the same time, again after 5 minutes",
    );
  });

  test("reads in Russian", async () => {
    await i18n.changeLanguage("ru");
    expect(words(important, [contact()])).toBe(
      "упоминание в треде и звонок на +10000000002 одновременно, ещё раз через 5 минут",
    );
  });

  test("names the Notification gateway, unavailable, for a call whose contact is gone", () => {
    expect(words(important, [])).toBe(
      "a Mention in the Thread and a call to Notification gateway (unavailable) at the same time, again after 5 minutes",
    );
  });

  test("takes only a confirmed contact of the gateway of the call", () => {
    const unconfirmed = contact({ confirmed: false, confirmed_at: null });
    const other = contact({ connection: { id: "CN00000000GW02", name: "Backup" } });
    expect(confirmedContact([unconfirmed, other], "CN00000000GW01")).toBeUndefined();
    expect(confirmedContact([unconfirmed, other], "CN00000000GW02")).toBe(other);
    expect(callGatewayId({ kind: "call", gateway_id: "CN00000000GW09" })).toBe("CN00000000GW09");
  });

  test("an Organization's call, which belongs to nobody, names its gateway", () => {
    expect(words(important, null)).toBe(
      "a Mention in the Thread and a call through Example Gateway at the same time, again after 5 minutes",
    );
    expect(
      words([call({ gateway: undefined, available: undefined }), wait(5), mention], null),
    ).toBe("a call through the Notification gateway, then a Mention in the Thread after 5 minutes");
  });
});

describe("other sequences", () => {
  test("three Mentions at the same moment", () => {
    expect(words([mention, mention, mention])).toBe("3 Mentions in the Thread at the same time");
  });

  test("a changed wait and the plural of minutes", async () => {
    expect(words([mention, wait(10), mention])).toBe(
      "a Mention in the Thread and again after 10 minutes if you did not answer",
    );
    expect(words([mention, wait(1), mention])).toContain("again after 1 minute ");
    await i18n.changeLanguage("ru");
    expect(words([mention, wait(1), mention])).toContain("через 1 минуту");
    expect(words([mention, wait(2), mention])).toContain("через 2 минуты");
    expect(words([mention, wait(11), mention])).toContain("через 11 минут");
    expect(words([mention, mention, mention])).toBe("3 упоминания в треде одновременно");
  });

  test("a later moment that is not a lone Mention names its actions", () => {
    expect(words([mention, wait(5), call(), wait(10), mention], [contact()])).toBe(
      "a Mention in the Thread, then a call to +10000000002 after 5 minutes, again after 10 minutes",
    );
  });

  test("a Mention after a call alone is not an again", () => {
    expect(words([call(), wait(5), mention], [contact()])).toBe(
      "a call to +10000000002, then a Mention in the Thread after 5 minutes",
    );
  });

  test("an empty rule has no words", () => {
    expect(words([])).toBe("");
  });
});
