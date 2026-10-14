// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The list editor of the outbound address policy in a real browser: a refusal at "/denied/1" shows next to the second
// denied entry and nowhere else; entries are added with the focus on them, and removed.

import { useEffect } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { I18nextProvider, useTranslation } from "react-i18next";
import { beforeEach, describe, expect, test } from "vitest";
import { render } from "vitest-browser-react";

import i18n from "../i18n";
import { ApiError, applyFieldErrors } from "../lib/api";
import { NetworkListEditor } from "./network-list-editor";
import { errorText } from "./organization-form";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

interface Values {
  allowed: string[];
  denied: string[];
}

const refusal = new ApiError(
  422,
  { title: "Unprocessable", errors: [{ pointer: "/denied/1", code: "invalid_format" }] },
  undefined,
);

/** The two lists as the Outbound addresses page holds them, with the refusal applied once. */
function Harness({ refuse, readOnly = false }: { refuse: boolean; readOnly?: boolean }) {
  const { t } = useTranslation();
  const form = useForm<Values>({
    defaultValues: {
      allowed: ["10.0.0.0/8"],
      denied: ["192.168.0.0/16", "not a network", "x.org"],
    },
  });
  useEffect(() => {
    if (refuse) {
      applyFieldErrors(
        refusal,
        form.setError,
        ["allowed.0", "denied.0", "denied.1", "denied.2"],
        (c) => c,
      );
    }
  }, [form, refuse]);
  const { errors } = form.formState;
  const allowed = useWatch({ control: form.control, name: "allowed" });
  const denied = useWatch({ control: form.control, name: "denied" });
  return (
    <form>
      {(["allowed", "denied"] as const).map((name) => (
        <Controller
          key={name}
          control={form.control}
          name={name}
          render={({ field }) => (
            <NetworkListEditor
              id={`list-${name}`}
              label={name === "allowed" ? "Allowed" : "Denied"}
              addLabel={`Add ${name}`}
              value={field.value}
              readOnly={readOnly}
              errors={(name === "allowed" ? allowed : denied).map((_, i) =>
                errorText(t, errors[name]?.[i]),
              )}
              onChange={field.onChange}
            />
          )}
        />
      ))}
      <output data-testid="denied">{denied.join("|")}</output>
    </form>
  );
}

async function renderHarness(refuse: boolean, readOnly = false) {
  return render(
    <I18nextProvider i18n={i18n}>
      <Harness refuse={refuse} readOnly={readOnly} />
    </I18nextProvider>,
  );
}

describe("network list editor", () => {
  test("a refusal at /denied/1 shows next to the second denied entry only", async () => {
    const screen = await renderHarness(true);
    const second = screen.getByRole("textbox", { name: "Denied, entry 2" });
    await expect.element(second).toHaveAttribute("aria-invalid", "true");
    await expect.element(second).toHaveAccessibleDescription("This value has the wrong format.");
    expect(screen.getByText("This value has the wrong format.").elements()).toHaveLength(1);
    await expect
      .element(screen.getByRole("textbox", { name: "Denied, entry 1" }))
      .toHaveAttribute("aria-invalid", "false");
    await expect
      .element(screen.getByRole("textbox", { name: "Allowed, entry 1" }))
      .toHaveAttribute("aria-invalid", "false");
  });

  test("adds an entry with the focus on it and removes one", async () => {
    const screen = await renderHarness(false);
    await screen.getByRole("button", { name: "Add denied" }).click();
    const fourth = screen.getByRole("textbox", { name: "Denied, entry 4" });
    await expect.element(fourth).toHaveFocus();
    await fourth.fill("10.20.0.0/16");
    await expect
      .element(screen.getByTestId("denied"))
      .toHaveTextContent("192.168.0.0/16|not a network|x.org|10.20.0.0/16");
    await screen.getByRole("button", { name: "Remove Denied, entry 2" }).click();
    await expect
      .element(screen.getByTestId("denied"))
      .toHaveTextContent("192.168.0.0/16|x.org|10.20.0.0/16");
  });

  test("read-only shows the entries without add or remove", async () => {
    const screen = await renderHarness(false, true);
    await expect
      .element(screen.getByRole("textbox", { name: "Denied, entry 1" }))
      .toHaveAttribute("readonly");
    expect(screen.getByRole("button").elements()).toHaveLength(0);
  });
});
