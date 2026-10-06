// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Translations (NFR-8): English and Russian, hierarchical keys, Russian plurals through i18next's plural rules. The
// language is the profile's, else the browser's, falling back to English (C-03.FR-12).

import { createInstance } from "i18next";
import { initReactI18next } from "react-i18next";

import en from "./locales/en.json";
import ru from "./locales/ru.json";

export const LANGUAGES = ["en", "ru"] as const;
export type Language = (typeof LANGUAGES)[number];

const i18n = createInstance();

/** The browser's language when Muster has it, else English. */
export function browserLanguage(languages: readonly string[] = navigator.languages): Language {
  for (const tag of languages) {
    const base = tag.toLowerCase().split("-")[0];
    const found = LANGUAGES.find((l) => l === base);
    if (found !== undefined) {
      return found;
    }
  }
  return "en";
}

/** Switches the UI to the profile's language, or the browser's when the profile names none. */
export function applyLanguage(profileLanguage: string | null | undefined): void {
  const language = LANGUAGES.find((l) => l === profileLanguage) ?? browserLanguage();
  document.documentElement.lang = language;
  if (i18n.language !== language) {
    void i18n.changeLanguage(language);
  }
}

void i18n.use(initReactI18next).init({
  resources: { en: { translation: en }, ru: { translation: ru } },
  lng: browserLanguage(),
  fallbackLng: "en",
  supportedLngs: [...LANGUAGES],
  // React escapes what it renders.
  interpolation: { escapeValue: false },
  returnNull: false,
});
document.documentElement.lang = i18n.language;

export default i18n;
