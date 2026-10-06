// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// A QR code drawn as an SVG path from the module matrix of lean-qr: no canvas, no inline style and no markup string,
// so it renders under the Content Security Policy. Dark modules on a white quiet zone in both themes, as scanners
// expect.

import { generate } from "lean-qr";
import { useMemo } from "react";

/** The path of the dark modules, one unit square each, offset by the quiet zone. */
export function qrPath(text: string, quietZone = 4): { path: string; size: number } {
  const code = generate(text);
  const parts: string[] = [];
  for (let y = 0; y < code.size; y++) {
    for (let x = 0; x < code.size; x++) {
      if (code.get(x, y)) {
        parts.push(`M${x + quietZone} ${y + quietZone}h1v1h-1z`);
      }
    }
  }
  return { path: parts.join(""), size: code.size + 2 * quietZone };
}

export function QrCode({
  value,
  label,
  className,
}: {
  value: string;
  label: string;
  className?: string;
}) {
  const { path, size } = useMemo(() => qrPath(value), [value]);
  return (
    <svg
      viewBox={`0 0 ${size} ${size}`}
      role="img"
      aria-label={label}
      className={className}
      shapeRendering="crispEdges"
    >
      <rect width={size} height={size} fill="#ffffff" />
      <path d={path} fill="#000000" />
    </svg>
  );
}
