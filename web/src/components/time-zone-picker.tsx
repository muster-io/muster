// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// The choice of an IANA time zone with a search over the names and the current offset of each zone, such as
// "Europe/Berlin (UTC+02:00)". A native select keeps the phone's own picker and needs no styles of its own.

import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { timeZones } from "../lib/time";
import { Input } from "./ui/input";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

/** The current offset of a time zone from UTC, such as "UTC+02:00"; UTC itself is "UTC+00:00". */
export function utcOffset(timeZone: string, at: Date): string {
  try {
    const name = new Intl.DateTimeFormat("en", { timeZone, timeZoneName: "longOffset" })
      .formatToParts(at)
      .find((p) => p.type === "timeZoneName")?.value;
    if (name === undefined || name === "GMT") {
      return "UTC+00:00";
    }
    return name.replace(/^GMT/, "UTC");
  } catch {
    return "";
  }
}

function zoneOption(zone: string, at: Date): { zone: string; text: string } {
  const offset = utcOffset(zone, at);
  return { zone, text: offset === "" ? zone : `${zone} (${offset})` };
}

export function TimeZonePicker({
  id,
  value,
  onChange,
  invalid,
  describedBy,
}: {
  id: string;
  value: string;
  onChange: (zone: string) => void;
  invalid?: boolean;
  describedBy?: string;
}) {
  const { t } = useTranslation();
  const [search, setSearch] = useState("");
  // The offsets are read once per page; UTC is always selectable.
  const [now] = useState(() => new Date());
  const listed = useMemo(() => {
    const known = timeZones();
    return [...(known.includes("UTC") ? [] : ["UTC"]), ...known].map((zone) =>
      zoneOption(zone, now),
    );
  }, [now]);
  // A stored zone the browser does not list, read at first or after a reload, stays selectable.
  const zones =
    value === "" || listed.some((z) => z.zone === value)
      ? listed
      : [zoneOption(value, now), ...listed];
  const needle = search.trim().toLowerCase();
  const shown =
    needle === ""
      ? zones
      : zones.filter((z) => z.zone === value || z.text.toLowerCase().includes(needle));
  return (
    <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-center">
      <Input
        type="search"
        aria-label={t("organization.general.timeZoneSearch")}
        placeholder={t("organization.general.timeZoneSearch")}
        value={search}
        autoComplete="off"
        spellCheck={false}
        className="sm:max-w-48"
        onChange={(e) => setSearch(e.target.value)}
      />
      <NativeSelect
        id={id}
        className="w-full min-w-0 sm:max-w-sm"
        value={value}
        aria-invalid={invalid}
        aria-describedby={describedBy}
        onChange={(e) => onChange(e.target.value)}
      >
        {shown.map((z) => (
          <NativeSelectOption key={z.zone} value={z.zone}>
            {z.text}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </div>
  );
}
