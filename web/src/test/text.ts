// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// For component tests of the Mentions grid: `toHaveTextContent` with a string matches the whole text of an element, so
// the texts a test reads sit in elements of their own.

import { page } from "vitest/browser";

/** The reason a target of the "Add" list is greyed, by the id of the option, such as "group" or "everyone-all". */
export function reasonOf(optionId: string) {
  return page.getByTestId(`mention-option-${optionId}`).getByTestId("mention-option-reason");
}
