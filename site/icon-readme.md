# Corresync icon

The Corresync mark is a fixed-color SVG tile designed by Fable and normalized
for this repository. Two opposing chevrons orbit one blue point:

- the chevrons suggest correspondence folds, a CLI caret, and bidirectional
  synchronization without borrowing provider branding;
- the blue point represents the local policy core shared by every account,
  provider, CLI call, and MCP tool;
- the four-element construction remains identifiable at favicon size.

## Assets

- `corresync-mark.svg`: canonical 256 × 256 mark for the README, Pages header,
  package metadata, and social artwork;
- `favicon.svg`: optically simplified 64 × 64 version for browser chrome.

Both assets are flat vectors with embedded accessible names and descriptions.
They contain no font, raster, script, gradient, animation, or external
reference.

## Palette

<!-- markdownlint-disable MD013 -->
| Token | Value | Use |
| --- | --- | --- |
| local navy | `#13233b` | Self-contained tile |
| correspondence cream | `#f2ede2` | Upper flow |
| action coral | `#e0574a` | Lower flow |
| core blue | `#3f7ad6` | Local shared core |
<!-- markdownlint-enable MD013 -->

## Usage

- Keep the navy tile; the mark is not designed as standalone colored strokes.
- Leave at least one-eighth of the tile width as surrounding space.
- Use the canonical mark at 32 px and above and the favicon below 32 px.
- Do not rotate, recolor, outline, add shadows, or place provider marks inside
  the tile.
- In an `<img>`, provide contextual alt text or an empty alt when adjacent text
  already says “Corresync.” Inline copies may retain the embedded
  `aria-labelledby` metadata.
