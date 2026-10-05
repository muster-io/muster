# The Muster mark

Three signals converge on one point: many alerts become one Alert Group. The mark reads from left to right and is
drawn on a 64×64 grid with an 8-unit stroke, so it stays legible at 16 px.

| File | Use |
|---|---|
| [`muster-mark.svg`](muster-mark.svg) | The mark on a light background. |
| [`muster-mark-on-dark.svg`](muster-mark-on-dark.svg) | The mark on a dark background. |
| [`muster-mark-tile.svg`](muster-mark-tile.svg) | The mark on an indigo tile: the favicon and the avatar of a Mattermost or Telegram bot. |
| [`muster-mark-mono.svg`](muster-mark-mono.svg) | One colour, taken from `currentColor`, for inline use. |

The SPA serves the tile as `/favicon.svg` and as `/muster-mark-256.png`, a 256 px PNG made from the tile with
`rsvg-convert -w 256 -h 256 muster-mark-tile.svg -o ../../web/public/muster-mark-256.png`; Mattermost posts use the
PNG as their `footer_icon`.

| Colour | Hex |
|---|---|
| Indigo, the lines and the tile | `#3B3F9E` |
| Light indigo, the lines on a dark background | `#A9ACF2` |
| Amber, the point | `#F5A524` |

The mark is licensed under AGPL-3.0-only, like the rest of Muster.
