// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The capture helper of the documentation screenshots (make docs-screenshots). capture() writes
// bin/docs-screenshots/images/<docsPage>/<name>.png, where docsPage is the page's source path under docs/ without .md,
// such as messengers/telegram, and name is NN-<slug> in the order the page shows the images; make docs-screenshots
// moves them to docs/assets/screenshots/ once every spec passed. It waits for the text the step names and for the
// fonts, never for an idle network, which the live-updates stream keeps busy; masks what must not be read, such as a
// token shown once; and can outline the element the step asks the reader to use, which it removes after the shot.

import { mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";

import { expect, type Locator, type Page } from "@playwright/test";

/** Where the specs write their images: bin/docs-screenshots/images/ of the checkout. */
const IMAGES = fileURLToPath(new URL("../../../bin/docs-screenshots/images/", import.meta.url));

/** The indigo of the Muster mark (docs/brand), which outlines the element a step asks the reader to use. */
const HIGHLIGHT = "#3b3f9e";

/** A neutral grey that covers what is masked, lighter than Playwright's magenta. */
const MASK = "#d4d4d8";

export interface CaptureOptions {
  /** Text, or an element, that must be visible before the shot: what the step shows. */
  waitFor?: string | Locator;
  /** Elements covered in the image, such as a token shown once. */
  mask?: Locator[];
  /** The element to take instead of the whole viewport. */
  locator?: Locator;
  /** The element the step asks the reader to use, outlined for the shot only. */
  highlight?: Locator;
}

/** Takes the screenshot name of the documentation page docsPage. */
export async function capture(
  page: Page,
  docsPage: string,
  name: string,
  options: CaptureOptions = {},
): Promise<void> {
  expect(docsPage, "the page's source path without .md").toMatch(/^[a-z0-9-]+(\/[a-z0-9-]+)*$/);
  expect(name, "NN-<slug>").toMatch(/^\d\d-[a-z0-9]+(-[a-z0-9]+)*$/);
  if (options.waitFor !== undefined) {
    const shown =
      typeof options.waitFor === "string"
        ? page.getByText(options.waitFor).first()
        : options.waitFor;
    await expect(shown).toBeVisible();
  }
  if (options.locator !== undefined) {
    await expect(options.locator).toBeVisible();
  }
  await page.evaluate(async () => {
    await document.fonts.ready;
  });
  // The pointer rests where it hovers nothing the shot shows.
  await page.mouse.move(0, 0);
  const path = `${IMAGES}${docsPage}/${name}.png`;
  await mkdir(dirname(path), { recursive: true });
  const outlined = options.highlight;
  if (outlined !== undefined) {
    await outlined.evaluate((el, color) => {
      if (!(el instanceof HTMLElement)) {
        return;
      }
      el.dataset.captureOutline = el.style.outline;
      el.dataset.captureOutlineOffset = el.style.outlineOffset;
      el.style.outline = `2px solid ${color}`;
      el.style.outlineOffset = "2px";
    }, HIGHLIGHT);
  }
  try {
    const shot = {
      path,
      animations: "disabled" as const,
      caret: "hide" as const,
      mask: options.mask ?? [],
      maskColor: MASK,
    };
    if (options.locator !== undefined) {
      await options.locator.screenshot(shot);
    } else {
      await page.screenshot(shot);
    }
  } finally {
    if (outlined !== undefined) {
      await outlined.evaluate((el) => {
        if (!(el instanceof HTMLElement)) {
          return;
        }
        el.style.outline = el.dataset.captureOutline ?? "";
        el.style.outlineOffset = el.dataset.captureOutlineOffset ?? "";
        delete el.dataset.captureOutline;
        delete el.dataset.captureOutlineOffset;
      });
    }
  }
}

/** The card, a section of a page, that holds the element. */
export function cardOf(element: Locator): Locator {
  return element.locator("xpath=ancestor::*[@data-slot='card'][1]");
}

/** Scrolls the page so that the element starts near the top of the viewport. */
export async function scrollToTop(element: Locator): Promise<void> {
  await element.evaluate((el) => {
    window.scrollBy(0, el.getBoundingClientRect().top - 16);
  });
}
