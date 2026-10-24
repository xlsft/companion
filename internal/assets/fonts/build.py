# Builds the static, Latin+Cyrillic subset faces embedded by this package
# from the upstream variable fonts (all SIL OFL 1.1, see OFL.txt). Gio
# renders only the default instance of a variable font, hence the static cuts.
#
#   python -m venv ft && ft/bin/pip install fonttools
#   ft/bin/python build.py <dir with GoogleSans.ttf GoogleSansCode.ttf Unbounded.ttf> .
#
# The CJK faces are Noto Sans CJK (SIL OFL 1.1) cut from the system
# collections (Regular and Bold, SC/JP/KR faces) down to the characters the
# zh/ja/ko catalogues use, so a new translation needs this rerun:
#
#   ft/bin/python build.py --cjk /usr/share/fonts/noto-cjk .
#
# Regular serves 400/500 requests and Bold serves 600/700 (Gio picks the
# nearest weight), which also covers the Unbounded headings. Text typed or
# received at runtime outside the subset falls to the system fonts.

import json, os, sys
from fontTools.ttLib import TTFont, TTCollection
from fontTools.varLib import instancer
from fontTools import subset
if sys.argv[1] == "--cjk":
    build_cjk = True
    cjk_dir, out = sys.argv[2], sys.argv[3]
else:
    build_cjk = False
    src, out = sys.argv[1], sys.argv[2]
# Latin, Latin-1, Latin Ext-A, Cyrillic, general punctuation, arrows,
# geometric shapes and math symbols used by status markers.
unicodes = "U+0020-007E,U+00A0-017F,U+0400-045F,U+0490-0491,U+2000-206F,U+20AC,U+2116,U+2190-21FF,U+2212,U+221A,U+2022,U+25A0-25FF,U+00D7"
jobs = [
    ("GoogleSans.ttf", "GoogleSans-Regular.ttf", {"wght": 400, "opsz": 18, "GRAD": 0}),
    ("GoogleSans.ttf", "GoogleSans-Medium.ttf", {"wght": 500, "opsz": 18, "GRAD": 0}),
    ("GoogleSans.ttf", "GoogleSans-SemiBold.ttf", {"wght": 600, "opsz": 18, "GRAD": 0}),
    ("GoogleSansCode.ttf", "GoogleSansCode-Regular.ttf", {"wght": 400, "MONO": 1}),
    ("GoogleSansCode.ttf", "GoogleSansCode-Medium.ttf", {"wght": 500, "MONO": 1}),
    ("Unbounded.ttf", "Unbounded-SemiBold.ttf", {"wght": 600}),
]
if not build_cjk:
    for f, name, loc in jobs:
        t = instancer.instantiateVariableFont(TTFont(f"{src}/{f}"), loc, updateFontNames=False)
        opts = subset.Options()
        opts.layout_features = ["kern", "liga", "calt", "ccmp", "locl", "mark", "mkmk", "tnum"]
        opts.name_IDs = ["*"]
        opts.notdef_outline = True
        s = subset.Subsetter(opts)
        s.populate(unicodes=subset.parse_unicodes(unicodes))
        s.subset(t)
        t.save(f"{out}/{name}")
        print(name)

def cjk():
    here = os.path.dirname(os.path.abspath(__file__))
    loc = os.path.join(here, "..", "..", "ui", "i18n", "locales")
    # Language names are listed in every UI language, so every face needs them.
    common = set("中文日本語한국어")
    # (catalogue, face index in the .ttc, output name)
    faces = [("zh", 2, "NotoSansCJKsc"), ("ja", 0, "NotoSansCJKjp"), ("ko", 1, "NotoSansCJKkr")]
    for code, idx, base in faces:
        text = "".join(json.load(open(os.path.join(loc, code + ".json"), encoding="utf-8")).values())
        # ASCII is Google Sans's job; keep everything else plus CJK and
        # fullwidth punctuation/forms.
        chars = {c for c in text if ord(c) > 0x7F} | common
        chars |= {chr(c) for c in range(0x3000, 0x3040)} | {chr(c) for c in range(0xFF01, 0xFF5F)}
        for weight in ("Regular", "Bold"):
            ttc = TTCollection(f"{cjk_dir}/NotoSansCJK-{weight}.ttc")
            t = ttc.fonts[idx]
            opts = subset.Options()
            opts.layout_features = ["kern", "ccmp"]
            opts.name_IDs = ["*"]
            opts.notdef_outline = True
            opts.hinting = False
            s = subset.Subsetter(opts)
            s.populate(unicodes=sorted(ord(c) for c in chars))
            s.subset(t)
            name = f"{base}-{weight}.otf"
            t.flavor = None
            t.save(f"{out}/{name}")
            print(name, len(chars), "chars")

if build_cjk:
    cjk()
