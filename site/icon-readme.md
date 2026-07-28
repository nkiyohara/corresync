# Corresync icon system

One mark, two drawings: correspondence (an airmail envelope) carried by
synchronization (two arrows chasing clockwise around it). The red dot is the
unread letter riding in the orbit's gap, one arrow always chasing it.

<!-- markdownlint-disable MD013 -->
| File | Role | Grid | Use at |
| --- | --- | --- | --- |
| `site/corresync-mark.svg` | Canonical mark | 128 | 32 px and up (docs, social, README, 512 px exports) |
| `site/favicon.svg` | Compact form | 64 | 16-32 px (browser tabs, pinned tabs) |
| `plugins/corresync/assets/icon.svg` | Plugin logo | 128 | Same drawing as the canonical mark, plugin-local copy |
<!-- markdownlint-enable MD013 -->

## Geometry

Everything is constructed, not eyeballed, on a center `c` = (64, 64):

- **Tile** - full-bleed rounded square, corner radius 28/128 (21.9%). A 2.5-unit
  rim stroke at 14% opacity defines the edge on dark UIs; it disappears on
  light.
- **Orbit** - two 130° arcs of radius 36, stroke 10, round trailing caps, with
  two 50° gaps on the diagonal through the badge (bearing ≈ 327° / 147°).
  Arrowheads are kite triangles: base is the radial segment ±6.75 at the arc
  end, tip on the arc circle 19.5° ahead, so each arrow follows the curve.
- **Envelope** - 42 x 29 (1.45:1), corner radius 4, centered at (64, 65): one
  unit below true center to offset the flap's visual weight. Flap is a stroke
  chevron (width 4.2, endpoints inset 6.2, apex at 52% of envelope height).
- **Airmail band** - five 45°-leaning bars (red, blue, red, blue, red),
  3.9 wide on a 6.5 pitch, centered, 3.4 above the envelope's bottom edge.
  Matches the site's `.airmail` CSS stripe direction and on-paper colors.
- **Badge** - radius 6 dot on the envelope's top-right corner, pushed 1.3 units
  out along the corner bearing, with a 2.6 navy keyline separating it from
  everything it overlaps. It sits inside the orbit's gap; the nearest arrowhead
  points at it with ≈ 4-unit clearance.

## Compact form (favicon)

Same composition redrawn on a 64 grid for 16 px legibility, not scaled down:

- Orbit grows to radius 24 (75% of tile) with 120° arcs; arrowheads shorten so
  the tip keeps ≥ 2-unit clearance from the dot at 16 px.
- Flap becomes a filled triangle - a stroke chevron smears at 16 px, a solid
  wedge keeps the envelope gestalt.
- Airmail band is dropped (it would be sub-pixel noise); the red dot alone
  carries the accent.
- Dot is proportionally ~2x the canonical size (≈ 3 px at 16 px).

## Color

<!-- markdownlint-disable MD013 -->
| Token | Hex | Role |
| --- | --- | --- |
| `--mark-bg` | `#0e1523` | Tile, flap, keylines |
| orbit blue | `#6b93ff` | Arcs and arrowheads (6.5:1 on tile) |
| paper cream | `#f2ede2` | Envelope (15:1 on tile) |
| unread red | `#e0574a` | Badge dot |
| `--stripe-red` / `--stripe-blue` | `#c03b2d` / `#2148cc` | Airmail band on cream |
<!-- markdownlint-enable MD013 -->

All colors are fixed (no `currentColor`): the mark is self-contained on its own
tile and needs no theme awareness. Both files carry `role="img"`,
`<title>`/`<desc>` wired via `aria-labelledby`, and contain no fonts, rasters,
scripts, or external references.

## Safe area and usage

- The tile is the mark; never strip the navy square (content assumes it).
- When placing the tile in UI, leave a margin of at least 1/8 tile width around
  it and round nested containers to ~22% corner radius to match.
- Use the canonical mark at 32 px and above; the favicon form below that.
  Don't add drop shadows, gradients, rotations, or extra colors.
- `site/index.html` references the canonical mark for both header and footer.
