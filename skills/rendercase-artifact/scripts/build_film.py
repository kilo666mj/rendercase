#!/usr/bin/env python3
"""Validate a film directory and package it with the Rendercase film player.

Usage: build_film.py FILM_DIR OUT_DIR

FILM_DIR holds scenes.json and optionally base.css, shared.js, score.js,
scene-<id>.js, <name>.html modules and fonts/<Stem>.<ext>. Writes a playable
bundle to OUT_DIR and OUT_DIR.zip with index.html at the archive root.
"""

import base64
import json
import re
import shutil
import sys
import zipfile
from pathlib import Path

PLAYER = Path(__file__).resolve().parent.parent / "assets" / "film-player"
ID = re.compile(r"^[a-z][a-z0-9-]*$")
MODULE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]*\.html$")
FONT = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]*\.(otf|ttf|woff|woff2)$")
FONT_TYPES = {".otf": "font/otf", ".ttf": "font/ttf", ".woff": "font/woff", ".woff2": "font/woff2"}
FIXED = {"scenes.json", "base.css", "shared.js", "score.js"}
# Required data fields of the stock scene types in stock/scenes.js.
STOCK = {
    "title": {"title"},
    "statement": {"title"},
    "stat": {"value", "label"},
    "list": {"title", "items"},
    "chips": {"title", "chips"},
    "code": {"title", "typed"},
    "quote": {"text"},
    "end": {"title"},
}
MAX_TOTAL = 600
MAX_BYTES = 20 * 1024 * 1024


class Invalid(ValueError):
    pass


def number(v, lo, hi, where):
    if not isinstance(v, (int, float)) or isinstance(v, bool) or not lo <= v <= hi:
        raise Invalid(f"{where}: must be a number from {lo} to {hi}")


def words(v):
    if isinstance(v, str):
        return len(v.split())
    if isinstance(v, dict):
        return sum(words(x) for x in v.values())
    if isinstance(v, list):
        return sum(words(x) for x in v)
    return 0


def reading_time(data):
    # Every entrance lands by about 1.5 s; then allow a calm 3 words a second.
    return round(min(14.0, max(5.0, 2.5 + words(data) / 3.0)), 1)


def inventory(film):
    files = {}
    for p in sorted(film.rglob("*")):
        rel = p.relative_to(film).as_posix()
        if p.is_symlink():
            raise Invalid(f"{rel}: symbolic links are not allowed")
        if p.is_dir():
            if rel != "fonts":
                raise Invalid(f"{rel}: only a fonts/ subdirectory is allowed")
            continue
        name = p.name
        ok = (rel in FIXED or (rel.startswith("scene-") and rel.endswith(".js")) or MODULE.match(rel)
              or (rel.startswith("fonts/") and FONT.match(name)))
        if not ok:
            raise Invalid(f"{rel}: not a film file (see references/film.md)")
        files[rel] = p
    if "scenes.json" not in files:
        raise Invalid("scenes.json is missing")
    if sum(p.stat().st_size for p in files.values()) > MAX_BYTES:
        raise Invalid("film is larger than 20 MB")
    return files


def validate(m, files):
    if not isinstance(m, dict):
        raise Invalid("scenes.json: must be an object")
    for k, lo, hi in (("width", 320, 3840), ("height", 320, 3840), ("fps", 1, 120)):
        number(m.get(k), lo, hi, k)
    if "palette" in m and (not isinstance(m["palette"], list) or len(m["palette"]) > 4
                           or not all(isinstance(c, str) and len(c) < 64 for c in m["palette"])):
        raise Invalid("palette: must be up to four CSS colors [ground, main, second, accent]")
    fonts = m.get("fonts", {})
    if not isinstance(fonts, dict) or set(fonts) - {"sans", "mono", "display"}:
        raise Invalid("fonts: may only set sans, mono and display")
    music = m.get("music", {})
    if music is not False:
        if not isinstance(music, dict):
            raise Invalid("music: must be an object or false")
        if "tempo" in music:
            number(music["tempo"], 50, 200, "music.tempo")
        if music.get("mode", "major") not in ("major", "minor"):
            raise Invalid("music.mode: must be major or minor")
    scenes = m.get("scenes")
    if not isinstance(scenes, list) or not 1 <= len(scenes) <= 60:
        raise Invalid("scenes: must be a list of 1 to 60 scenes")
    seen, total = set(), 0.0
    for i, s in enumerate(scenes):
        where = f"scenes[{i}]"
        if not isinstance(s, dict):
            raise Invalid(f"{where}: must be an object")
        sid = s.get("id")
        if not isinstance(sid, str) or not ID.match(sid) or sid in seen:
            raise Invalid(f"{where}.id: must be unique lower-case letters, digits and '-', starting with a letter")
        seen.add(sid)
        if not isinstance(s.get("name"), str) or not s["name"].strip():
            raise Invalid(f"{where}.name: must label the timeline")
        kind, data = s.get("type"), s.get("data", {})
        if not isinstance(data, dict):
            raise Invalid(f"{where}.data: must be an object")
        if f"scene-{sid}.js" in files:
            if kind:
                raise Invalid(f"{where}: has both a type and scene-{sid}.js")
        elif kind in STOCK:
            missing = STOCK[kind] - data.keys()
            if missing:
                raise Invalid(f"{where}.data: {kind} needs {', '.join(sorted(missing))}")
        elif kind and "shared.js" in files:
            pass  # a custom type registered with kit.type in shared.js; checked when the film loads
        else:
            raise Invalid(f"{where}: needs scene-{sid}.js or a type ({', '.join(STOCK)})")
        if "dur" not in s:
            if not kind:
                raise Invalid(f"{where}.dur: required for a scene written in JavaScript")
            s["dur"] = reading_time(data)
        number(s["dur"], 0.5, 120, f"{where}.dur")
        total += s["dur"]
    if total > MAX_TOTAL:
        raise Invalid(f"film is {total:.0f} s; the limit is {MAX_TOTAL} s")


def wrap(src):
    # Each film script gets its own scope, so every scene can declare the same top-level names.
    return ";(function () {\n" + src + "\n})();\n"


def build(film, out):
    files = inventory(film)
    m = json.loads(files["scenes.json"].read_text(encoding="utf-8"))
    validate(m, files)
    if out.exists() or out.with_suffix(".zip").exists():
        raise Invalid(f"{out} or its .zip already exists; choose a new output directory")
    shutil.copytree(PLAYER, out, ignore=shutil.ignore_patterns("example"))
    (out / "film").mkdir()

    # Fonts are inlined as data: URIs. Rendercase frames films in a sandbox with an opaque
    # origin, where font files are CORS requests that the content origin does not allow.
    fonts = [(p.stem, p) for rel, p in files.items() if rel.startswith("fonts/")]
    modules = {rel: p.read_text(encoding="utf-8") for rel, p in files.items() if MODULE.match(rel)}
    data = {"manifest": m, "modules": modules}
    (out / "film-data.js").write_text("window.__FILM__ = " + json.dumps(data, ensure_ascii=False) + ";\n", encoding="utf-8")

    styles = []
    if fonts:
        css = "".join(
            f'@font-face {{ font-family: "{stem}"; font-display: block; '
            f'src: url("data:{FONT_TYPES[p.suffix]};base64,{base64.b64encode(p.read_bytes()).decode()}"); }}\n'
            for stem, p in fonts)
        (out / "film" / "fonts.css").write_text(css, encoding="utf-8")
        styles.append("film/fonts.css")
    if "base.css" in files:
        shutil.copyfile(files["base.css"], out / "film" / "base.css")
        styles.append("film/base.css")
    scripts = ["shared.js"] if "shared.js" in files else []
    scripts += [f"scene-{s['id']}.js" for s in m["scenes"] if f"scene-{s['id']}.js" in files]
    scripts += ["score.js"] if "score.js" in files else []
    for name in scripts:
        (out / "film" / name).write_text(wrap(files[name].read_text(encoding="utf-8")), encoding="utf-8")

    page = (out / "index.html").read_text(encoding="utf-8")
    page = page.replace("<!--STYLES-->", "\n".join(f'<link rel="stylesheet" href="{s}">' for s in styles))
    page = page.replace("<!--SCRIPTS-->", "\n".join(f'<script src="film/{s}"></script>' for s in scripts))
    (out / "index.html").write_text(page, encoding="utf-8")

    zip_path = out.with_suffix(".zip")
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as z:
        for p in sorted(out.rglob("*")):
            if p.is_file():
                z.write(p, p.relative_to(out).as_posix())
    return zip_path, sum(s["dur"] for s in m["scenes"])


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    try:
        zip_path, total = build(Path(sys.argv[1]), Path(sys.argv[2]))
    except (Invalid, json.JSONDecodeError, UnicodeDecodeError) as e:
        sys.exit(f"invalid film: {e}")
    print(f"{zip_path} ({total:.1f} s)")


if __name__ == "__main__":
    main()
