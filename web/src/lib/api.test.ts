// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The field errors of a refusal on the fields of a form: the whole JSON pointer names the field, nested and array
// pointers included, a pointer below a listed field falls back to it, and the rest is left to the form as a whole.

import { beforeEach, describe, expect, test, vi } from "vitest";

import i18n from "../i18n";
import { ApiError, applyFieldErrors, fieldErrorText } from "./api";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

function refusal(...errors: [pointer: string, code: string][]): ApiError {
  return new ApiError(
    422,
    { title: "Unprocessable", errors: errors.map(([pointer, code]) => ({ pointer, code })) },
    undefined,
  );
}

describe("applyFieldErrors", () => {
  test("a nested pointer names the nested field", () => {
    const setError = vi.fn();
    const unmatched = applyFieldErrors(
      refusal(["/retention/alert_group_summaries_days", "retention_order"]),
      setError,
      ["retention.alert_details_days", "retention.alert_group_summaries_days"],
      (code) => code,
    );
    expect(unmatched).toEqual([]);
    expect(setError).toHaveBeenCalledExactlyOnceWith("retention.alert_group_summaries_days", {
      type: "retention_order",
      message: "retention_order",
    });
  });

  test("an array pointer names the entry", () => {
    const setError = vi.fn();
    applyFieldErrors(
      refusal(["/allowed/2", "invalid_format"], ["/severity_mapping/1/value", "duplicate"]),
      setError,
      [
        "allowed.0",
        "allowed.1",
        "allowed.2",
        "severity_mapping.1.value",
        "severity_mapping.1.level",
      ],
      (code) => code,
    );
    expect(setError).toHaveBeenNthCalledWith(1, "allowed.2", {
      type: "invalid_format",
      message: "invalid_format",
    });
    expect(setError).toHaveBeenNthCalledWith(2, "severity_mapping.1.value", {
      type: "duplicate",
      message: "duplicate",
    });
  });

  test("decodes the escapes of a pointer", () => {
    const setError = vi.fn();
    applyFieldErrors(
      refusal(["/labels/a~1b~0c", "required"]),
      setError,
      ["labels.a/b~c"],
      (c) => c,
    );
    expect(setError).toHaveBeenCalledWith("labels.a/b~c", {
      type: "required",
      message: "required",
    });
  });

  test("a pointer below a listed field goes to the longest field it lies under", () => {
    const setError = vi.fn();
    const unmatched = applyFieldErrors(
      refusal(["/permissions/2", "unsupported"], ["/outgoing_heartbeat/proxy/address", "required"]),
      setError,
      ["permissions", "outgoing_heartbeat", "outgoing_heartbeat.proxy"],
      (code) => code,
    );
    expect(unmatched).toEqual([]);
    expect(setError).toHaveBeenNthCalledWith(1, "permissions", {
      type: "unsupported",
      message: "unsupported",
    });
    expect(setError).toHaveBeenNthCalledWith(2, "outgoing_heartbeat.proxy", {
      type: "required",
      message: "required",
    });
  });

  test("returns the codes that match no field, and a field is never matched by a mere prefix of its name", () => {
    const setError = vi.fn();
    const unmatched = applyFieldErrors(
      refusal(["/oidc_token_grace_seconds", "out_of_range"], ["/names", "required"]),
      setError,
      ["name", "oidc_token_grace_days"],
      (code) => code,
    );
    expect(unmatched).toEqual(["out_of_range", "required"]);
    expect(setError).not.toHaveBeenCalled();
  });

  test("puts the translated message on the field", () => {
    const setError = vi.fn();
    applyFieldErrors(
      refusal(["/retention/alert_group_summaries_days", "retention_order"]),
      setError,
      ["retention.alert_group_summaries_days"],
      (code) => fieldErrorText(i18n.t, code),
    );
    expect(setError).toHaveBeenCalledWith("retention.alert_group_summaries_days", {
      type: "retention_order",
      message: "Keep Alert Group summaries at least as long as alert details.",
    });
    expect(fieldErrorText(i18n.t, "duplicate")).toBe("This value is listed twice.");
    expect(fieldErrorText(i18n.t, "out_of_range")).toBe("This value is out of range.");
  });
});
