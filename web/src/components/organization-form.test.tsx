// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The update an Organization page sends (C-33.FR-11): everything as read, except the mark, which is left out so that
// the API keeps it, and the other read-only fields.

import { describe, expect, test } from "vitest";

import { getGetOrganizationResponseMock } from "../api/gen/endpoints/organization/organization.faker";
import { organizationInput } from "./organization-form";

describe("organizationInput", () => {
  test("leaves the mark out of the update of a marked Organization", () => {
    const marked = { ...getGetOrganizationResponseMock(), managed_by: "terraform" };
    const input = organizationInput(marked);
    expect("managed_by" in input).toBe(false);
    expect(input.name).toBe(marked.name);
  });

  test("leaves it out of the update of an unmarked one, and keeps the patch", () => {
    const unmarked = { ...getGetOrganizationResponseMock(), managed_by: null };
    const input = organizationInput(unmarked, { name: "Other" });
    expect("managed_by" in input).toBe(false);
    expect(input.name).toBe("Other");
  });
});
