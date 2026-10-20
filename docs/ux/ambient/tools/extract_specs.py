#!/usr/bin/env python3
"""Build a UX text contract for each mockup.

Reads every NN-*.html mockup in the parent folder and writes ux-specs/NN-*.json
with the labels a user sees (tabs, headings, buttons, links, column headers,
chips, segmented controls, status pills, form labels), in reading order.
Annotation circles and the yellow notes boxes are skipped.

The contract has two lists:
  required  structural UI text that must appear in the built screen, in order
  example   data from the mockup (names, numbers); it shows intent only and is
            not checked, because the fixture data will differ

Usage: python3 tools/extract_specs.py
"""
from html.parser import HTMLParser
import json, pathlib, re

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "ux-specs"

# class or tag -> role. Order of checks matters: the first match wins.
STRUCTURAL = [
    ("amtabs-a", "tab"), ("tabs-a", "tab"), ("seg-span", "toggle"),
    ("btn", "button"), ("lnk", "link"), ("chip", "chip"),
    ("qh", "heading"), ("ghead-h2", "heading"), ("lbl", "label"),
    ("th", "column"), ("h2", "heading"), ("h3", "heading"), ("steps-span", "step"),
    ("pill", "pill"),
]
EXAMPLE = [("h1", "title"), ("ans", "answer"), ("mono", "data"), ("l", "item"), ("now", "data")]
SKIP = {"notes", "annot", "side", "top", "crumb"}
FIXED_PILLS = {"Configured", "Not set", "Recommended", "Inherited", "Not available", "Draft", "Checked",
               "Applied", "Live", "Verified", "Failed", "DENY", "ALLOW", "Split", "Differs", "Missing"}
# Roles whose absence is always a defect. Links and pills depend on the data
# (which tasks are set), so a missing one is reported for a person to judge.
MUST = {"tab", "toggle", "heading", "column", "button", "label", "step", "chip"}
VIEWPORT = re.compile(r"window-size=(\d+),(\d+)")


class P(HTMLParser):
    def __init__(self):
        super().__init__()
        self.stack = []  # (tag, classes, parent_tag_classes)
        self.items = []
        self.buf = None

    def handle_starttag(self, tag, attrs):
        if tag in ("br", "img", "link", "meta", "input"):
            return
        cls = (dict(attrs).get("class") or "").split()
        parent = self.stack[-1] if self.stack else ("", [])
        self.stack.append((tag, cls, parent))

    def handle_endtag(self, tag):
        while self.stack:
            t, cls, parent = self.stack.pop()
            if t == tag:
                break

    def skipped(self):
        return any(set(c) & SKIP for _, c, _ in self.stack)

    def role(self):
        if not self.stack:
            return None, None
        tag, cls, (ptag, pcls, _) = self.stack[-1]
        keys = set(cls) | {tag} | {f"{c}-{tag}" for c in pcls}
        for k, r in STRUCTURAL:
            if k in keys:
                return "required", r
        for k, r in EXAMPLE:
            if k in keys:
                return "example", r
        return None, None

    def handle_data(self, data):
        text = " ".join(data.split())
        if not text or self.skipped():
            return
        kind, role = self.role()
        if not kind:
            return
        if role == "pill" and text.strip("●✓✕!🔒 ") not in FIXED_PILLS:
            kind = "example"
        if re.search(r"kind-mesh\d|reviews|bookinfo|ratings|productpage", text) and role != "heading":
            kind = "example"  # names from the mockup data, not UI labels
        text = re.sub(r"\s*·?\s*\d+$", "", text) if role in ("tab", "toggle") else text  # drop counts
        self.items.append({"kind": kind, "role": role, "text": text})


def main():
    OUT.mkdir(exist_ok=True)
    for f in sorted(ROOT.glob("[0-9][0-9]*.html")):
        p = P()
        p.feed(f.read_text())
        req, ex, seen = [], [], set()
        for it in p.items:
            key = (it["kind"], it["role"], it["text"])
            if key in seen and it["role"] in ("pill", "link", "button"):
                continue  # repeated row actions: keep the first occurrence
            seen.add(key)
            (req if it["kind"] == "required" else ex).append({"role": it["role"], "text": it["text"]})
        for r in req:
            r["severity"] = "must" if r["role"] in MUST else "should"
        spec = {"screen": f.stem, "mockup_html": f.name, "mockup_png": f.stem + ".png",
                "required": req, "example": ex}
        (OUT / f"{f.stem}.json").write_text(json.dumps(spec, indent=2, ensure_ascii=False) + "\n")
        print(f"{f.stem}: {len(req)} required, {len(ex)} example")


if __name__ == "__main__":
    main()
