#!/usr/bin/env python3
"""Regenerate the self-hosted UI font subsets.

The UI ships self-hosted Inter Variable and JetBrains Mono Variable. The full
families are ~422 kB together and Inter is preloaded before first paint, which
made it the largest single first-paint asset — larger than any JS chunk. This
script cuts them to the character ranges the console can actually render:
Latin (basic, -1, Ext-A/B, Ext Additional), punctuation, currency, letterlike,
arrows, math, box drawing, geometric/misc symbols and dingbats. CJK text falls
back to system fonts (Inter/Mono have no CJK glyphs either way), so nothing is
lost for Chinese.

Usage (requires `pip install fonttools brotli`):

    py frontend/scripts/subset-fonts.py            # rewrite the committed files
    py frontend/scripts/subset-fonts.py --check    # verify coverage + sizes only

`check-bundle.mjs` fails if the total .woff2 bytes exceed 300 kB, so restoring a
full family without re-running this script is caught in CI.
"""
from __future__ import annotations

import argparse
import os
import sys

from fontTools import subset
from fontTools.ttLib import TTFont

FONT_DIR = os.path.join(os.path.dirname(__file__), "..", "src", "styles", "fonts")
FONTS = ["Inter-Variable.woff2", "JetBrainsMono-Variable.woff2"]

# Latin + the symbols a console UI actually prints (arrows, bullets, checks,
# box drawing for ASCII art, comparison operators, currency, punctuation).
UNICODES = ",".join(
    [
        "U+0000-00FF",  # Basic Latin + Latin-1 Supplement
        "U+0100-024F",  # Latin Extended-A/B
        "U+02B0-02FF",  # spacing modifiers
        "U+0300-036F",  # combining diacritics
        "U+1E00-1EFF",  # Latin Extended Additional
        "U+2000-206F",  # general punctuation (… – — ‘ ’ “ ” •)
        "U+20A0-20CF",  # currency
        "U+2100-214F",  # letterlike (™ №)
        "U+2190-21FF",  # arrows (→ ← ↑ ↓)
        "U+2200-22FF",  # math (≤ ≥ ≠ ∞)
        "U+2300-23FF",  # technical (⌘ ⏱)
        "U+2500-257F",  # box drawing
        "U+25A0-25FF",  # geometric shapes (● ▶ ■)
        "U+2600-26FF",  # misc symbols (★ ⚠)
        "U+2700-27BF",  # dingbats (✓ ✗)
        "U+2B00-2BFF",  # misc symbols and arrows
        "U+FE00-FE0F",  # variation selectors
        "U+FFFD",       # replacement character
    ]
)

# Glyphs the console relies on; a subset that drops one of these is a regression
# (the browser would draw tofu instead of the symbol). Per font, because the
# mono family never shipped dingbats or stars (verified against the originals).
SAMPLES = {
    "Inter-Variable.woff2": "Aaz09·→←↑↓✓✗…–—“”‘’≤≥≠★●▶⚠©®™€£¥§¶±×÷",
    "JetBrainsMono-Variable.woff2": "Aaz09·→←↑↓…–—‘’“”≤≥≠●▶©®™€£¥§¶±×÷",
}


def font_path(name: str) -> str:
    return os.path.join(FONT_DIR, name)


def missing_glyphs(path: str, sample: str) -> list[str]:
    cmap = TTFont(path).getBestCmap()
    return [ch for ch in sample if ord(ch) not in cmap]


def subset_font(name: str) -> None:
    path = font_path(name)
    tmp = path + ".subset"
    before = os.path.getsize(path)
    args = [
        path,
        f"--unicodes={UNICODES}",
        "--layout-features=*",
        "--flavor=woff2",
        f"--output-file={tmp}",
    ]
    subset.main(args)
    missing = missing_glyphs(tmp, SAMPLES[name])
    if missing:
        os.remove(tmp)
        sys.exit(f"{name}: subset lost glyphs {missing!r}; widen UNICODES")
    os.replace(tmp, path)
    after = os.path.getsize(path)
    print(f"{name}: {before // 1024} kB -> {after // 1024} kB")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="verify coverage and sizes without rewriting")
    args = parser.parse_args()

    for name in FONTS:
        path = font_path(name)
        if not os.path.exists(path):
            sys.exit(f"missing font: {path}")
        missing = missing_glyphs(path, SAMPLES[name])
        if missing:
            sys.exit(f"{name}: missing sample glyphs {missing!r}")
        print(f"{name}: {os.path.getsize(path) // 1024} kB, coverage OK")
        if not args.check:
            subset_font(name)


if __name__ == "__main__":
    main()
