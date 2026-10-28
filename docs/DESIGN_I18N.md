# Interface translation

Nanolathe's own interface can be read in another language than English:
Ukrainian (issue #144, user-authorized 2026-10-21) and Simplified Chinese.
This document is the design.

## Catalogs

`internal/i18n` holds one catalog per language, `locales/<code>.json`,
embedded in the binary. A catalog maps the English text the interface already
draws to its translation, the way retail's `translate.tdf` keys its sections
by the English words. There are no identifiers to keep in step with the
code:

- text with no entry is drawn as written, so a new or reworded string stays
  readable in English until the catalog catches up;
- an entry whose English no longer appears is harmless.

A key containing `{}` or `{#}` is a pattern. `{}` matches a run of text and
`{#}` a number; the translation places the runs at its own `{}` in order, or
at `{1}`, `{2}`… by number when the language orders them differently. A run a
pattern captures is translated too (two levels deep), so a sentence assembled
from translated parts, such as an action label inside a description, reads as
one. Longer keys are tried first, so a specific sentence wins over a general
one. Exact strings are looked up directly; pattern results are kept in a
bounded cache.

English is the source language and has no catalog. `SetLanguage` selects a
catalog; one that fails to load leaves English with a notice.

## Where text is translated

Translation is presentation only. Nothing the simulation, saves or replays
compute reads it.

The Nanolathe screen sets all its text through `internal/platform/screenkit`.
`Font.Measure`, `Font.Draw` and `Font.Wrap` translate a run before measuring
or setting it, so the layout measures what is drawn. Several kinds of text
set `Style.Verbatim` instead:

- text the player typed;
- keyboard legends;
- lines `Wrap` already translated;
- the screen's kept capitals (`nlUICache.upperCase`, translated once and
  then kept);
- a fitted line (`nlUICache.fit` translates before it shrinks or elides, so
  eliding never leaves an English fragment no entry matches);
- a unit viewer label, which the viewer translates before it fits the label
  on the CPU (`unitViewerFitText`).

The unit viewer's CPU measurements use the same Verbatim choice as drawing.
Its info rows store their final spelling: UI labels and notes are translated
before measuring or wrapping, while authored descriptions, weapon names,
side and movement keys retain their spelling. Drawing those stored rows is
Verbatim. A language change rebuilds the retained rows, including when an
optional font download finishes while the viewer is open. Unit names, IDs,
content names and search input also draw and measure Verbatim, even when
their text happens to match a catalog key or pattern. Long viewer text wraps
between complete Unicode characters.

The kept capitals, wrapped lines and fitted lines start over when the
language changes.

`Font.Wrap` breaks Latin and Cyrillic text at spaces only. A run of
full-width characters, such as Chinese, may also break between any two of
them; a closing mark written against the text before it (`。`, `，`, `”`…)
travels with that text, and an opening mark (`“`, `（`…) with the text after
it, so neither starts or ends a line. Marks set off by spaces, such as the
` · ` separator, break as words.

Where a translation runs longer than the English the screen was laid out for,
the layout gives way:

- a card's value line shrinks to the room left of its lamps;
- the "What it changes" list starts its second column past the first
  column's widest line.

## Fonts

The film faces (Barlow) have no Cyrillic or Han. `tools/film-font-cyrillic`
appends Cyrillic glyphs from Fira Sans to the shared atlases, scaled to
Barlow's cap height. `tools/film-font-cjk` writes the full-width characters
a catalog uses from Noto Sans SC into separate `display-cjk` and `body-cjk`
atlases, leaving the shared pair unchanged. These glyphs are rasterized at
about half the Latin resolution: each carries a `scale` (face units per atlas
texel) that the film rasterizer and `screenkit` apply to its size, border and
mip choice. `internal/film/assets/README.md` records the pinned source,
tool versions and reproduction command.

The executable embeds the optional pair and decodes it on the first Chinese
selection. The browser fetches it on demand from the current host build's
content-addressed assets, verifies all four files, and delivers them together.
English and Ukrainian never request or decode these additions. While a fetch
runs, the previous language stays active. A failure leaves that interface
readable and the Chinese choice available for another Apply; the saved choice
is retained for the next launch too. Choosing another language cancels pending
activation. Successful completion activates Chinese on a host step, and every
screen refreshes its retained fonts before drawing. Loaded glyphs are kept for
the rest of the process; ordinary draws do not upload them again.

## Choosing the language

- The `language` setting holds a language code, or nothing for English.
- `--language <code>` overrides it for one run.
- The Language card on the Game page changes the setting on Apply. Each
  language is named and described in its own words, so a player who cannot
  read the running language can still find theirs. Chinese is labelled in
  English until its optional glyphs are available.

## Adding a language

1. Add `internal/i18n/locales/<code>.json`, mapping the screen's English to
   the language. A partial catalog works: missing entries stay English.
2. Add the code and the language's own name for itself to `i18n.Languages`.
3. If the language needs letters the film faces lack, provide them as
   `tools/film-font-cyrillic` does for Cyrillic or in an optional atlas as
   `tools/film-font-cjk` does for Chinese. Keep the optional download paired
   with the browser host build (DESIGN_BROWSER_HOST §4 contract 7).

## Scope

Text Nanolathe draws with `screenkit` is translated wherever the catalog
has an entry for it. The Ukrainian catalog covers the Nanolathe screen; the
Chinese catalog also covers the unit viewer's own labels. Unit, weapon and
description text in the viewer stays as the content has it, independently of
catalog entries that happen to match it.

These stay as the game's files have them:
- the menus drawn in the original game's art;
- the battle interface;
- unit names and descriptions;
- mission briefings.

Translating them needs glyphs in the game's GAF/FNT fonts, and the text
retail draws from `translate.tdf` and the GUI files. That is not part of
this change.
