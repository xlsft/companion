# Builds the static, Latin+Cyrillic subset faces embedded by this package
# from the upstream variable fonts (all SIL OFL 1.1, see OFL.txt). Gio
# renders only the default instance of a variable font, hence the static cuts.
#
#   python -m venv ft && ft/bin/pip install fonttools
#   ft/bin/python build.py <dir with GoogleSans.ttf GoogleSansCode.ttf Unbounded.ttf> .

import sys
from fontTools.ttLib import TTFont
from fontTools.varLib import instancer
from fontTools import subset
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
