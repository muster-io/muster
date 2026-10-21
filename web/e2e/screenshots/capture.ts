// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The capture helper of the documentation screenshots (make docs-screenshots). capture() writes
// bin/docs-screenshots/images/<docsPage>/<name>.png, where docsPage is the page's source path under docs/ without .md,
// such as messengers/telegram, and name is NN-<slug> in the order the page shows the images; make docs-screenshots
// moves them to docs/assets/screenshots/ once every spec passed. It waits for the text the step names and for the
// fonts, never for an idle network, which the live-updates stream keeps busy; masks what must not be read, such as a
// token shown once; and can outline the element the step asks the reader to use, which it removes after the shot.
// So that two runs take the same pixels, it rounds the scroll position to whole pixels, takes the focus away, and for
// the shot alone rewrites what changes from run to run — public ids, times, latencies, signed button values — to fixed,
// realistic values (stabilize), which it puts back afterwards.

import { mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";

import { expect, type Frame, type Locator, type Page } from "@playwright/test";

/** Where the specs write their images: bin/docs-screenshots/images/ of the checkout. */
const IMAGES = fileURLToPath(new URL("../../../bin/docs-screenshots/images/", import.meta.url));

/** The indigo of the Muster mark (docs/brand), which outlines the element a step asks the reader to use. */
const HIGHLIGHT = "#3b3f9e";

/** A neutral grey that covers what is masked, lighter than Playwright's magenta. */
const MASK = "#d4d4d8";

/** The type prefixes of the public ids (api/README.md, "public_id format"). */
const PREFIXES = [
  "AE",
  "AG",
  "AK",
  "AR",
  "CN",
  "CR",
  "DE",
  "DS",
  "KR",
  "NE",
  "NK",
  "NT",
  "PT",
  "RG",
  "RQ",
  "RT",
  "SA",
  "SN",
  "SR",
  "SS",
  "ST",
  "TB",
  "TE",
];

/** The fixed moment that the shots show as "now", in the browser's time zone: Wednesday, October 14, 2026, 09:40. */
const NOW = { year: 2026, month: 9, day: 14, hour: 9, minute: 40 };

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
  if (options.locator !== undefined) {
    await options.locator.evaluate((el) => {
      el.scrollIntoView({ block: "nearest" });
    });
  }
  await page.evaluate(() => {
    window.scrollTo(Math.round(window.scrollX), Math.round(window.scrollY));
    if (document.activeElement instanceof HTMLElement) {
      document.activeElement.blur();
    }
  });
  // The hover, focus and entrance transitions that the last steps started finish before the shot; endless ones,
  // such as the pulse of the live indicator, are not waited for.
  await page.waitForFunction(() =>
    document
      .getAnimations()
      .every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity),
  );
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
  const frames = page.frames().filter((f) => f !== page.mainFrame());
  const fixed: Fixed = { prefixes: PREFIXES, now: NOW };
  try {
    await (options.locator ?? page.locator("body")).evaluate(stabilize, fixed);
    for (const frame of frames) {
      await stabilizeFrame(frame, fixed);
    }
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
    for (const frame of [page.mainFrame(), ...frames]) {
      await frame.evaluate(restore).catch(() => undefined);
    }
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

/** What stabilize writes: the public id prefixes and the moment shown as now. */
interface Fixed {
  prefixes: string[];
  now: { year: number; month: number; day: number; hour: number; minute: number };
}

/** Stabilizes the document of a frame, such as the preview of a Root message; a frame that is gone is left. */
async function stabilizeFrame(frame: Frame, fixed: Fixed): Promise<void> {
  await frame
    .locator("body")
    .evaluate(stabilize, fixed)
    .catch(() => undefined);
}

/**
 * Rewrites, in the page's browser, the text under root that changes from run to run to fixed, realistic values, and
 * keeps the originals for restore: public ids get a fixed body after their prefix; absolute times as the UI and the
 * messages write them ("Oct 21, 2026, 12:35", "Oct 21, 2026", "2026-10-21 09:46 UTC", Unix seconds or milliseconds
 * near now) keep their distance from now, in steps of five minutes or whole days, from the fixed now; latencies ("5 ms",
 * "Took 12 ms") take fixed values in turn; a running duration shows "1 min"; signed values of at least 20 characters in
 * JSON strings, such as callback_data, and the random end of an Idempotency-Key take fixed characters of the same
 * length. Text nodes and the values of inputs and text areas are rewritten; nothing else changes.
 */
function stabilize(root: Element, fixed: Fixed): void {
  const now = Date.now();
  const step = 5 * 60_000;
  const day = 24 * 60 * 60_000;
  const base = new Date(
    fixed.now.year,
    fixed.now.month,
    fixed.now.day,
    fixed.now.hour,
    fixed.now.minute,
  ).getTime();
  const shift = (t: number): number => base + Math.round((t - now) / step) * step;
  // The helpers stay inside: the function runs in the browser, which sees nothing of this module.
  // oxlint-disable-next-line unicorn/consistent-function-scoping -- serialized into the page
  const midnight = (t: number): number => {
    const d = new Date(t);
    return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  };
  const shiftDay = (t: number): number =>
    midnight(base) + Math.round((midnight(t) - midnight(now)) / day) * day;
  const months = [
    "Jan",
    "Feb",
    "Mar",
    "Apr",
    "May",
    "Jun",
    "Jul",
    "Aug",
    "Sep",
    "Oct",
    "Nov",
    "Dec",
  ];
  const dateTime = new Intl.DateTimeFormat("en-US", {
    dateStyle: "medium",
    timeStyle: "short",
    hourCycle: "h23",
  });
  const date = new Intl.DateTimeFormat("en-US", { dateStyle: "medium" });
  // oxlint-disable-next-line unicorn/consistent-function-scoping -- serialized into the page
  const pad = (n: number): string => String(n).padStart(2, "0");
  const latencies = [12, 4, 3, 6, 5, 8];
  let latency = 0;
  let seed = 0;
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  const token = (length: number, alphabet = chars): string => {
    seed += 1;
    let x = (seed * 2654435761) >>> 0;
    let out = "";
    for (let i = 0; i < length; i++) {
      x = (Math.imul(x, 1103515245) + 12345) >>> 0;
      out += alphabet[(x >>> 16) % alphabet.length];
    }
    return out;
  };
  const month = "(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)";
  const rules: [RegExp, (...m: string[]) => string][] = [
    [
      new RegExp(`\\b${month} (\\d{1,2}), (\\d{4}), (\\d{1,2}):(\\d{2})\\b`, "g"),
      (_, mo = "", d = "", y = "", h = "", mi = "") =>
        dateTime.format(new Date(shift(new Date(+y, months.indexOf(mo), +d, +h, +mi).getTime()))),
    ],
    [
      new RegExp(`\\b${month} (\\d{1,2}), (\\d{4})\\b(?!, \\d)`, "g"),
      (_, mo = "", d = "", y = "") =>
        date.format(new Date(shiftDay(new Date(+y, months.indexOf(mo), +d).getTime()))),
    ],
    [
      /\b(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}) UTC\b/g,
      (_, y = "", mo = "", d = "", h = "", mi = "") => {
        const t = new Date(shift(Date.UTC(+y, +mo - 1, +d, +h, +mi)));
        return `${t.getUTCFullYear()}-${pad(t.getUTCMonth() + 1)}-${pad(t.getUTCDate())} ${pad(t.getUTCHours())}:${pad(t.getUTCMinutes())} UTC`;
      },
    ],
    [/\b1\d{12}\b/g, (m) => (Math.abs(+m - now) < day ? String(shift(+m)) : m)],
    [
      /\b1\d{9}\b/g,
      (m) => (Math.abs(+m * 1000 - now) < day ? String(Math.floor(shift(+m * 1000) / 1000)) : m),
    ],
    [/\b\d+ ms\b/g, () => `${latencies[latency++ % latencies.length]} ms`],
    [
      /(test-[A-Z]{2}[0-9A-Z]{12}-)([A-Za-z0-9_-]+)/g,
      (_, head = "", tail = "") => head + token(tail.length),
    ],
    [
      /"([A-Za-z0-9_-]{20,})"/g,
      (all, value = "") =>
        /\d/.test(value) && /[A-Za-z]/.test(value) ? `"${token(value.length)}"` : all,
    ],
    // Mattermost's ids, such as a post's: 26 characters of lower-case letters and digits.
    [
      /\b(?=[a-z0-9]*\d)(?=[a-z0-9]*[a-z])[a-z0-9]{26}\b/g,
      () => token(26, "abcdefghijklmnopqrstuvwxyz0123456789"),
    ],
    [
      new RegExp(`\\b(${fixed.prefixes.join("|")})[0-9A-HJKMNP-TV-Z]{12}\\b`, "g"),
      (_, prefix = "") => `${prefix}K7M3QX9P2RTA`,
    ],
  ];
  const rewrite = (text: string): string =>
    rules.reduce(
      (t, [re, f]) =>
        t.replace(re, (...m: unknown[]) =>
          f(...m.filter((x): x is string => typeof x === "string")),
        ),
      text,
    );
  const saved: { node: Node; text: string }[] = [];
  const fields: { el: HTMLInputElement | HTMLTextAreaElement; value: string }[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
    if (n instanceof Text) {
      nodes.push(n);
    }
  }
  for (const n of nodes) {
    const text = n.nodeValue ?? "";
    const next = n.parentElement?.closest('time[data-testid="duration"]') ? "1 min" : rewrite(text);
    if (next !== text) {
      saved.push({ node: n, text });
      n.nodeValue = next;
    }
  }
  for (const el of root.querySelectorAll("input, textarea")) {
    if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
      const next = rewrite(el.value);
      if (next !== el.value) {
        fields.push({ el, value: el.value });
        el.value = next;
      }
    }
  }
  const restoreAll = (): void => {
    for (const { node, text } of saved) {
      node.textContent = text;
    }
    for (const { el, value } of fields) {
      el.value = value;
    }
  };
  Object.assign(window, { musterCaptureRestore: restoreAll });
}

/** Puts back what stabilize rewrote in the frame's document. */
function restore(): void {
  const put: unknown = Reflect.get(window, "musterCaptureRestore");
  Reflect.deleteProperty(window, "musterCaptureRestore");
  if (typeof put === "function") {
    put();
  }
}
