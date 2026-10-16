"""Run with: python3 -m unittest discover -s skills/rendercase-artifact/scripts"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
import zipfile
from pathlib import Path

import build_film as b

EXAMPLE = b.PLAYER / "example"


class BuildFilmTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        self.film = self.tmp / "film"
        shutil.copytree(EXAMPLE, self.film)

    def manifest(self, edit):
        p = self.film / "scenes.json"
        m = json.loads(p.read_text())
        edit(m)
        p.write_text(json.dumps(m))

    def build(self):
        return b.build(self.film, self.tmp / "out")

    def test_example_builds_with_root_entrypoint_and_paced_scenes(self):
        zip_path, total = self.build()
        names = zipfile.ZipFile(zip_path).namelist()
        self.assertIn("index.html", names)
        self.assertIn("film-data.js", names)
        self.assertFalse(any(n.startswith("example/") for n in names))
        scenes = json.loads((self.tmp / "out" / "film-data.js").read_text()[len("window.__FILM__ = "):-2])["manifest"]["scenes"]
        self.assertTrue(all(5 <= s["dur"] <= 14 for s in scenes))
        self.assertAlmostEqual(total, sum(s["dur"] for s in scenes))

    def test_custom_scripts_are_scoped_and_fonts_inlined(self):
        for sid in ("a", "b"):
            (self.film / f"scene-{sid}.js").write_text(f"const {{ seg }} = kit;\nkit.scene('{sid}', {{ render() {{}} }});\n")
        (self.film / "fonts").mkdir()
        (self.film / "fonts" / "Brand-Bd.woff2").write_bytes(b"wOF2fake")
        self.manifest(lambda m: m["scenes"].insert(1, {"id": "a", "name": "A", "dur": 3}) or m["scenes"].insert(2, {"id": "b", "name": "B", "dur": 3}))
        self.build()
        out = self.tmp / "out"
        page = (out / "index.html").read_text()
        self.assertLess(page.index("film/scene-a.js"), page.index("film/scene-b.js"))
        self.assertIn('url("data:font/woff2;base64,', (out / "film" / "fonts.css").read_text())
        if shutil.which("node"):
            both = (out / "film" / "scene-a.js").read_text() + (out / "film" / "scene-b.js").read_text()
            js = "const kit = { scene() {} };\n" + both
            subprocess.run(["node", "-e", js], check=True)

    def test_rejects_bad_films(self):
        cases = {
            "id": lambda: self.manifest(lambda m: m["scenes"][1].update(id="Bad id")),
            "duplicate": lambda: self.manifest(lambda m: m["scenes"][1].update(id="title")),
            "stock data": lambda: self.manifest(lambda m: m["scenes"][1]["data"].pop("value")),
            "no scene code": lambda: self.manifest(lambda m: m["scenes"][1].pop("type")),
            "too long": lambda: self.manifest(lambda m: [s.update(dur=100) for s in m["scenes"]]),
            "stray file": lambda: (self.film / ".env").write_text("SECRET=1"),
            "subdirectory": lambda: (self.film / "notes").mkdir(),
        }
        if hasattr(os, "symlink"):
            cases["symlink"] = lambda: os.symlink("/etc/hostname", self.film / "logo.html")
        for name, break_it in cases.items():
            with self.subTest(name):
                shutil.rmtree(self.film)
                shutil.copytree(EXAMPLE, self.film)
                break_it()
                with self.assertRaises(b.Invalid):
                    self.build()

    def test_refuses_to_overwrite_output(self):
        (self.tmp / "out").mkdir()
        with self.assertRaises(b.Invalid):
            self.build()


if __name__ == "__main__":
    unittest.main()
