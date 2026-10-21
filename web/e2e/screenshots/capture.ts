// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The capture helper of the documentation screenshots (make docs-screenshots). capture() writes
// bin/docs-screenshots/images/<docsPage>/<name>.png, where docsPage is the page's source path under docs/ without .md,
// such as messengers/telegram, and name is NN-<slug> in the order the page shows the images; make docs-screenshots
// moves them to docs/assets/screenshots/ once every spec passed. It waits for the text the step names and for the
// fonts, never for an idle network, which the live-updates stream keeps busy; masks what must not be read, such as a
// token shown once; and can outline the element the step asks the reader to use, which it removes after the shot.
// So that two runs take the same pixels, it rounds the scroll position to whole pixels, takes the focus away, and for
// the shot alone rewrites what changes from run to run — public ids, times, latencies, signed button values, tokens
// and secrets shown once — to fixed, realistic values (stabilize), which it puts back afterwards; a spec adds the values
// only it knows, such as the id of a replica. Before each shot it fails when the image would show the loopback address
// of a fake server of muster dev.

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
  /** Pixels of the page around the locator that the image takes too, so that a field is not cut at its edges. */
  padding?: number;
  /**
   * The element at whose bottom edge the image ends, the image starting at the top of the viewport: a list then shows
   * only its newest rows, such as those the spec made itself.
   */
  bottom?: Locator;
  /**
   * The element 16 pixels above whose top edge the image starts, the image ending at the bottom of the viewport, or at
   * the bottom element: the lower part of a page that the viewport shows only below what an earlier image showed.
   */
  top?: Locator;
  /**
   * The moment, in milliseconds since the epoch, that the times shown keep their distance from instead of now: for a
   * clock spec, the business time when it started, so that the real time its steps take does not move them.
   */
  reference?: number;
  /**
   * Texts that change from run to run and that only the spec knows, such as the id of a replica, each with the fixed
   * text the image shows instead.
   */
  replace?: [string, string][];
  /**
   * Elements hidden for the shot alone, such as the rows of a list that earlier specs of the run left, which would
   * otherwise change the widths of the columns the image shows.
   */
  hide?: Locator;
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
  // Hidden for the shot alone, before the layout settles; put back in the finally below. A failure in between fails
  // the spec anyway.
  await options.hide?.evaluateAll((els) => {
    for (const el of els) {
      if (el instanceof HTMLElement) {
        el.dataset.captureDisplay = el.style.display;
        el.style.display = "none";
      }
    }
  });
  await page.evaluate(async () => {
    await document.fonts.ready;
  });
  // The pointer rests where it hovers nothing the shot shows.
  await page.mouse.move(0, 0);
  if (options.locator !== undefined) {
    await options.locator.evaluate((el, margin) => {
      el.scrollIntoView({ block: "nearest" });
      // With padding, the margin around the element comes into view too.
      const r = el.getBoundingClientRect();
      // A pixel more than needed, as the scroll position is rounded to whole pixels below.
      if (margin === 0) {
        return;
      }
      if (r.top - margin < 0) {
        window.scrollBy(0, Math.floor(r.top - margin) - 1);
      } else if (r.bottom + margin > window.innerHeight) {
        window.scrollBy(0, Math.ceil(r.bottom + margin - window.innerHeight) + 1);
      }
    }, options.padding ?? 0);
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
  const fixed: Fixed = {
    prefixes: PREFIXES,
    now: NOW,
    replace: options.replace ?? [],
    reference: options.reference ?? null,
  };
  const padding = options.padding ?? 0;
  try {
    const target = padding > 0 ? undefined : options.locator;
    await (target ?? page.locator("body")).evaluate(stabilize, fixed);
    if (target !== undefined) {
      // The rest of the page too, whose edges show around a dialog or a card; the element itself keeps its first pass.
      await target.evaluate((el) => {
        el.setAttribute("data-capture-stabilized", "");
      });
      await page.locator("body").evaluate(stabilize, fixed);
    }
    for (const frame of frames) {
      await stabilizeFrame(frame, fixed);
    }
    await expectNoFakeAddress(page, frames, options, padding);
    const shot = {
      path,
      animations: "disabled" as const,
      caret: "hide" as const,
      mask: options.mask ?? [],
      maskColor: MASK,
    };
    if (options.locator !== undefined && padding > 0) {
      const box = await options.locator.boundingBox();
      expect(box, "the locator's box").not.toBeNull();
      const viewport = page.viewportSize() ?? { width: 1280, height: 800 };
      const x = Math.floor((box?.x ?? 0) - padding);
      const y = Math.floor((box?.y ?? 0) - padding);
      const right = Math.ceil((box?.x ?? 0) + (box?.width ?? 0) + padding);
      const bottom = Math.ceil((box?.y ?? 0) + (box?.height ?? 0) + padding);
      // The element with its padding fits the viewport, or the image would be cut: as for top and bottom, it fails.
      expect({ x, y, right, bottom }, "the element with its padding, within the viewport").toEqual({
        x: Math.max(0, x),
        y: Math.max(0, y),
        right: Math.min(viewport.width, right),
        bottom: Math.min(viewport.height, bottom),
      });
      await page.screenshot({ ...shot, clip: { x, y, width: right - x, height: bottom - y } });
    } else if (options.bottom !== undefined || options.top !== undefined) {
      const { start, end } = await verticalClip(page, options);
      const viewport = page.viewportSize() ?? { width: 1280, height: 800 };
      await page.screenshot({
        ...shot,
        clip: { x: 0, y: start, width: viewport.width, height: end - start },
      });
    } else if (options.locator !== undefined) {
      await options.locator.screenshot(shot);
    } else {
      await page.screenshot(shot);
    }
  } finally {
    for (const frame of [page.mainFrame(), ...frames]) {
      await frame.evaluate(restore).catch(() => undefined);
    }
    await page
      .locator("[data-capture-display]")
      .evaluateAll((els) => {
        for (const el of els) {
          if (el instanceof HTMLElement) {
            el.style.display = el.dataset.captureDisplay ?? "";
            delete el.dataset.captureDisplay;
          }
        }
      })
      .catch(() => undefined);
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

/** A link of the main navigation, such as Routes. */
export function nav(page: Page, name: string): Locator {
  return page.getByRole("navigation", { name: "Main" }).getByRole("link", { name, exact: true });
}

/** A link of the Organization's navigation, such as Lookup tables. */
export function organizationNav(page: Page, name: string): Locator {
  return page
    .getByRole("navigation", { name: "Organization" })
    .getByRole("link", { name, exact: true });
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

/** What stabilize writes: the public id prefixes, the moment shown as now and the spec's own replacements. */
interface Fixed {
  prefixes: string[];
  now: { year: number; month: number; day: number; hour: number; minute: number };
  replace: [string, string][];
  reference: number | null;
}

/**
 * The rows of the viewport that an image with a top or a bottom element takes. Both edges must lie within the
 * viewport, so that an image never ends or starts in the middle of a row because the viewport cut it.
 */
async function verticalClip(
  page: Page,
  options: CaptureOptions,
): Promise<{ start: number; end: number }> {
  const viewport = page.viewportSize() ?? { width: 1280, height: 800 };
  let start = 0;
  let end = viewport.height;
  if (options.top !== undefined) {
    const box = await options.top.boundingBox();
    expect(box, "the box of the element the image starts at").not.toBeNull();
    start = Math.floor((box?.y ?? 0) - 16);
    expect(
      start,
      "the top of the element the image starts at, within the viewport",
    ).toBeGreaterThanOrEqual(0);
  }
  if (options.bottom !== undefined) {
    const box = await options.bottom.boundingBox();
    expect(box, "the box of the element the image ends at").not.toBeNull();
    end = Math.ceil((box?.y ?? 0) + (box?.height ?? 0) + 8);
    expect(
      end,
      "the bottom of the element the image ends at, within the viewport",
    ).toBeLessThanOrEqual(viewport.height);
  }
  return { start, end };
}

/** The loopback address of a fake server of muster dev, which no image may show (S-106). */
const FAKE_ADDRESS = "127\\.0\\.0\\.1:1\\d{4}|localhost:1[89]\\d{3}";

/**
 * Fails when a text of the area the image takes — the locator's box with its padding, the viewport down to the bottom
 * element, else the viewport — or of a frame names a fake server's address, unless a mask covers it.
 */
async function expectNoFakeAddress(
  page: Page,
  frames: Frame[],
  options: CaptureOptions,
  padding: number,
): Promise<void> {
  const viewport = page.viewportSize() ?? { width: 1280, height: 800 };
  const box = (await options.locator?.boundingBox()) ?? { x: 0, y: 0, ...viewport };
  const vertical =
    options.bottom !== undefined || options.top !== undefined
      ? await verticalClip(page, options)
      : undefined;
  const area = {
    left: box.x - padding,
    top: vertical?.start ?? box.y - padding,
    right: box.x + box.width + padding,
    bottom: vertical?.end ?? box.y + box.height + padding,
  };
  const mask = options.mask ?? [];
  const masked = (await Promise.all(mask.map((m) => m.elementHandles()))).flat();
  const found = await page.evaluate(
    ({ pattern, shot, covers }) => {
      const fake = new RegExp(pattern);
      const shows = (r: DOMRect): boolean =>
        r.width > 0 &&
        r.height > 0 &&
        r.right > shot.left &&
        r.left < shot.right &&
        r.bottom > shot.top &&
        r.top < shot.bottom;
      const hidden = (n: Node): boolean => covers.some((m) => m.contains(n));
      const out: string[] = [];
      const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
        const text = n.nodeValue ?? "";
        if (!fake.test(text) || hidden(n)) {
          continue;
        }
        const range = document.createRange();
        range.selectNodeContents(n);
        if ([...range.getClientRects()].some(shows)) {
          out.push(text);
        }
      }
      for (const el of document.querySelectorAll("input, textarea")) {
        if (
          (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) &&
          fake.test(el.value) &&
          !hidden(el) &&
          shows(el.getBoundingClientRect())
        ) {
          out.push(el.value);
        }
      }
      return out;
    },
    { pattern: FAKE_ADDRESS, shot: area, covers: masked },
  );
  for (const frame of frames) {
    const text = await frame
      .locator("body")
      .innerText()
      .catch(() => "");
    const element = await frame.frameElement().catch(() => null);
    const covered =
      element === null ||
      (await element.evaluate((el, handles) => handles.some((m) => m.contains(el)), masked));
    if (new RegExp(FAKE_ADDRESS).test(text) && !covered) {
      found.push(text);
    }
  }
  expect(found, "a fake server's address in the image").toEqual([]);
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
 * keeps the originals for restore: the spec's own replacements come first and stay as written, the rules rewriting
 * the rest of their text; public ids get a fixed body after their
 * prefix; absolute times as the UI and the messages write them ("Oct 21, 2026, 12:35", with seconds or without,
 * "Oct 21, 2026", "2026-10-21 09:46 UTC", "2026-10-21" in a date field, an HTTP date, "since 12:35", "14:17:21" on its
 * own, Unix seconds or milliseconds near now) keep their distance from now — or from the spec's reference — in steps of
 * five minutes, dates in whole days, from the fixed now; latencies
 * ("5 ms", "Took 12 ms") take fixed values in turn; a running duration shows "1 min"; tokens Muster issues
 * (mstr_int_, mstr_pat_, mstr_sat_), Signing secrets (whsec_), the token of a password setup link, signed values of
 * at least 20 characters in JSON strings, such as callback_data, and the random end
 * of an Idempotency-Key take fixed characters of the same length, one value the same wherever it shows; and the fake
 * webhook receiver shows as https://hooks.example.org. Text nodes and the values of inputs and text areas are
 * rewritten; nothing else changes.
 */
function stabilize(root: Element, fixed: Fixed): void {
  const now = fixed.reference ?? Date.now();
  const step = 5 * 60_000;
  const day = 24 * 60 * 60_000;
  const base = new Date(
    fixed.now.year,
    fixed.now.month,
    fixed.now.day,
    fixed.now.hour,
    fixed.now.minute,
  ).getTime();
  // The helpers stay inside: the function runs in the browser, which sees nothing of this module.
  // oxlint-disable-next-line unicorn/consistent-function-scoping -- serialized into the page
  const midnight = (t: number): number => {
    const d = new Date(t);
    return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  };
  // In calendar days, so that a change to or from summer time on the way does not move the date; the time of day stays.
  const shiftDay = (t: number): number => {
    const days = Math.round((midnight(t) - midnight(now)) / day);
    const b = new Date(base);
    const d = new Date(t);
    return new Date(
      b.getFullYear(),
      b.getMonth(),
      b.getDate() + days,
      d.getHours(),
      d.getMinutes(),
    ).getTime();
  };
  // A time keeps its distance from now, in steps of five minutes. A time that does not follow now, such as the expiry
  // of a token on a chosen date, would not reproduce this way: the spec shows it through its own replacements.
  const shift = (t: number): number => base + Math.round((t - now) / step) * step;
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
  // A value that shows more than once in a shot, such as a token in its field and in a snippet, keeps one rewrite.
  const memo = new Map<string, string>();
  const same = (value: string, make: () => string): string => {
    const known = memo.get(value);
    if (known !== undefined) {
      return known;
    }
    const made = make();
    memo.set(value, made);
    return made;
  };
  const month = "(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)";
  // oxlint-disable-next-line unicorn/consistent-function-scoping -- serialized into the page
  const escape = (text: string): string => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const rules: [RegExp, (...m: string[]) => string][] = [
    // An HTTP date, as in the Date header of an answer: "Wed, 21 Oct 2026 12:35:07 GMT".
    [
      new RegExp(
        `\\b(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun), (\\d{2}) ${month} (\\d{4}) (\\d{2}):(\\d{2}):(\\d{2}) GMT\\b`,
        "g",
      ),
      (_, d = "", mo = "", y = "", h = "", mi = "", se = "") => {
        const t = new Date(shift(Date.UTC(+y, months.indexOf(mo), +d, +h, +mi, +se)));
        const weekday = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"][t.getUTCDay()] ?? "";
        return `${weekday}, ${pad(t.getUTCDate())} ${months[t.getUTCMonth()] ?? ""} ${t.getUTCFullYear()} ${pad(t.getUTCHours())}:${pad(t.getUTCMinutes())}:00 GMT`;
      },
    ],
    // The fake webhook receiver of muster dev shows as the receiver of the examples.
    [/http:\/\/127\.0\.0\.1:18093/g, () => "https://hooks.example.org"],
    [/\b127\.0\.0\.1:18093\b/g, () => "hooks.example.org"],
    // Tokens Muster issues: their prefix, then 52 characters of lower-case base32.
    [
      /\b(mstr_(?:int|pat|sat)_)([a-z2-7]{52})\b/g,
      (all, prefix = "") => same(all, () => prefix + token(52, "abcdefghijklmnopqrstuvwxyz234567")),
    ],
    // The Signing secret of an outgoing webhook: whsec_ and 32 bytes in base64.
    [
      /\b(whsec_)([A-Za-z0-9+/]{43}=)/g,
      (all, head = "") => same(all, () => `${head + token(43)}=`),
    ],
    // The token of a password setup link: …/password-setup#token=….
    [
      /(#token=)([A-Za-z0-9_-]+)/g,
      (all, head = "", tail = "") => same(all, () => head + token(tail.length)),
    ],
    // A date near now as a date field holds it, such as the expiry of a token: 2027-01-12.
    [
      /\b(20\d\d)-(\d\d)-(\d\d)\b(?![ T]\d)/g,
      (m, y = "", mo = "", d = "") => {
        const t = new Date(+y, +mo - 1, +d).getTime();
        if (Math.abs(t - now) > 400 * day) {
          return m;
        }
        const s = new Date(shiftDay(t));
        return `${s.getFullYear()}-${pad(s.getMonth() + 1)}-${pad(s.getDate())}`;
      },
    ],
    // A time of day with seconds on its own, as the Request log shows it under its date: "14:17:21".
    [
      /^(\d{2}):(\d{2}):(\d{2})$/g,
      (_, h = "", mi = "") => {
        const d = new Date(now);
        const t = new Date(
          shift(new Date(d.getFullYear(), d.getMonth(), d.getDate(), +h, +mi).getTime()),
        );
        return `${pad(t.getHours())}:${pad(t.getMinutes())}:00`;
      },
    ],
    // A time of today, such as the last Heartbeat signal: "since 09:35", "expires at 09:55".
    [
      /\b(since|expires at) (\d{2}):(\d{2})\b/g,
      (_, word = "", h = "", mi = "") => {
        const d = new Date(now);
        const t = new Date(
          shift(new Date(d.getFullYear(), d.getMonth(), d.getDate(), +h, +mi).getTime()),
        );
        return `${word} ${pad(t.getHours())}:${pad(t.getMinutes())}`;
      },
    ],
    [
      // With seconds too, as a record of the Request log shows its time; the seconds become :00.
      new RegExp(`\\b${month} (\\d{1,2}), (\\d{4}), (\\d{1,2}):(\\d{2})((?::\\d{2})?)\\b`, "g"),
      (_, mo = "", d = "", y = "", h = "", mi = "", seconds = "") =>
        dateTime.format(new Date(shift(new Date(+y, months.indexOf(mo), +d, +h, +mi).getTime()))) +
        (seconds === "" ? "" : ":00"),
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
  // The spec's own replacements come first. What they wrote stays as written, so that a fixed time such as the end of
  // a Snooze preset is not moved again, and the rules then rewrite the rest of the text around it.
  const rewrite = (text: string): string => {
    const kept: string[] = [];
    const marked = fixed.replace.reduce(
      (t, [from, to]) =>
        t.replace(new RegExp(escape(from), "g"), () => {
          kept.push(to);
          return `\uE000${kept.length - 1}\uE000`;
        }),
      text,
    );
    const ruled = rules.reduce(
      (t, [re, f]) =>
        t.replace(re, (...m: unknown[]) =>
          f(...m.filter((x): x is string => typeof x === "string")),
        ),
      marked,
    );
    return ruled.replace(/\uE000(\d+)\uE000/g, (_, i = "") => kept[+i] ?? "");
  };
  const saved: { node: Node; text: string }[] = [];
  const fields: { el: HTMLInputElement | HTMLTextAreaElement; value: string }[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes: Text[] = [];
  for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
    if (n instanceof Text) {
      nodes.push(n);
    }
  }
  // oxlint-disable-next-line unicorn/consistent-function-scoping -- serialized into the page
  const done = (el: Element | null | undefined): boolean =>
    (el?.closest("[data-capture-stabilized]") ?? null) !== null;
  for (const n of nodes) {
    if (done(n.parentElement)) {
      continue;
    }
    const text = n.nodeValue ?? "";
    const next = n.parentElement?.closest('time[data-testid="duration"]') ? "1 min" : rewrite(text);
    if (next !== text) {
      saved.push({ node: n, text });
      n.nodeValue = next;
    }
  }
  for (const el of root.querySelectorAll("input, textarea")) {
    if ((el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) && !done(el)) {
      const next = rewrite(el.value);
      if (next !== el.value) {
        fields.push({ el, value: el.value });
        el.value = next;
      }
    }
  }
  // A second pass over the rest of the page puts back its own changes and then those of the first.
  const before: unknown = Reflect.get(window, "musterCaptureRestore");
  const restoreAll = (): void => {
    for (const { node, text } of saved) {
      node.textContent = text;
    }
    for (const { el, value } of fields) {
      el.value = value;
    }
    for (const el of document.querySelectorAll("[data-capture-stabilized]")) {
      el.removeAttribute("data-capture-stabilized");
    }
    if (typeof before === "function") {
      before();
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
