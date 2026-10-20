#!/usr/bin/env python3
"""Compare a built Flightbay screen with its mockup.

For each screen it:
  1. screenshots the running app at the mockup's viewport (headless Chrome),
  2. writes a side-by-side PNG: mockup on the left, app on the right,
  3. dumps the rendered DOM and checks the ux-specs/<screen>.json contract:
     every "must" label present and in the same order, "should" labels reported.

Exit code is 1 if any "must" label is missing or out of order.

Start the app from source first (serves web/ from disk, real cluster actions disabled):
  cd ~/code/solo/flightbay
  FLIGHTBAY_AMBIENT_PREVIEW=$PWD/flightbay-mesh1-objects.tar.zst \
    go test ./internal/server -run TestAmbientBrowserPreview -v -timeout 20m
  # copy the printed AMBIENT_PREVIEW=http://127.0.0.1:PORT/?token=ambient-ui-test#ambient

Then:
  python3 tools/ux_compare.py --base 'http://127.0.0.1:PORT/?token=ambient-ui-test' \
      --screen 01-service-configuration
  python3 tools/ux_compare.py --base ... --all

Routes come from tools/routes.json; edit the Service and object names to match
the fixture you loaded.
"""
import argparse, json, pathlib, re, subprocess, sys, tempfile
from html.parser import HTMLParser

ROOT = pathlib.Path(__file__).resolve().parent.parent
CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"


def chrome(args):
    return subprocess.run([CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars",
                           "--virtual-time-budget=8000", *args], capture_output=True, text=True, timeout=120)


class Text(HTMLParser):
    """Visible text in document order, skipping script/style and hidden elements."""
    def __init__(self):
        super().__init__(); self.out = []; self.hide = 0; self.stack = []
    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        hidden = tag in ("script", "style", "template") or "hidden" in a or \
            "hidden" in (a.get("class") or "").split() or "display:none" in (a.get("style") or "").replace(" ", "")
        if tag in ("br", "img", "input", "meta", "link", "hr"):
            return
        self.stack.append(hidden); self.hide += hidden
    def handle_endtag(self, tag):
        if self.stack:
            self.hide -= self.stack.pop()
    def handle_data(self, d):
        t = " ".join(d.split())
        if t and not self.hide:
            self.out.append(t)


def check(spec, dom):
    p = Text(); p.feed(dom)
    stream = " \u241f ".join(p.out)  # unit separator keeps labels apart
    pos, rows, failed = 0, [], False
    for r in spec["required"]:
        needle = r["text"]
        i = stream.find(needle, pos)
        if i >= 0:
            pos = i + len(needle); rows.append(("ok", r))
            continue
        anywhere = stream.find(needle) >= 0
        status = "order" if anywhere else "missing"
        if r["severity"] == "must":
            failed = True
        rows.append((status, r))
    return rows, failed


def side_by_side(mock_png, app_png, out_png, label_l="MOCKUP", label_r="APP"):
    from PIL import Image, ImageDraw
    a, b = Image.open(mock_png).convert("RGB"), Image.open(app_png).convert("RGB")
    h = max(a.height, b.height) + 40
    im = Image.new("RGB", (a.width + b.width + 20, h), "white")
    im.paste(a, (0, 40)); im.paste(b, (a.width + 20, 40))
    d = ImageDraw.Draw(im)
    d.rectangle([0, 0, a.width, 36], fill="#fff4c2"); d.text((12, 12), label_l, fill="black")
    d.rectangle([a.width + 20, 0, im.width, 36], fill="#d9f2f5"); d.text((a.width + 32, 12), label_r, fill="black")
    im.save(out_png)


def run(base, screen, routes, outdir):
    spec = json.loads((ROOT / "ux-specs" / f"{screen}.json").read_text())
    route = routes[screen]
    url = base.split("#")[0] + route["hash"]
    w, h = route.get("viewport", [1600, 1100])
    out = outdir / screen; out.mkdir(parents=True, exist_ok=True)
    app_png = out / "app.png"
    chrome([f"--window-size={w},{h}", f"--screenshot={app_png}", url])
    dom = chrome([f"--window-size={w},{h}", "--dump-dom", url]).stdout
    (out / "dom.html").write_text(dom)
    rows, failed = check(spec, dom)
    mock_png = ROOT / spec["mockup_png"]
    if app_png.exists() and mock_png.exists():
        side_by_side(mock_png, app_png, out / "side-by-side.png")
    lines = [f"# {screen}", "", f"- URL: `{url}`", f"- Mockup: `{mock_png}`",
             f"- Side by side: `{out / 'side-by-side.png'}`", "",
             "| Result | Severity | Role | Label |", "|---|---|---|---|"]
    for status, r in rows:
        mark = {"ok": "ok", "order": "OUT OF ORDER", "missing": "MISSING"}[status]
        lines.append(f"| {mark} | {r['severity']} | {r['role']} | {r['text']} |")
    lines += ["", "Labels the app shows that are not in the mockup are not detected by this script.",
              "Compare the side-by-side image for extra elements, layout, spacing and visual weight."]
    (out / "report.md").write_text("\n".join(lines) + "\n")
    bad = [r for s, r in rows if s != "ok"]
    print(f"{screen}: {'FAIL' if failed else 'pass'} · {len(bad)} difference(s) · {out / 'report.md'}")
    return failed


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True, help="app URL including ?token=…")
    ap.add_argument("--screen", action="append", default=[])
    ap.add_argument("--all", action="store_true")
    ap.add_argument("--out", default=str(ROOT / "ux-review"))
    a = ap.parse_args()
    routes = json.loads((ROOT / "tools" / "routes.json").read_text())
    screens = sorted(routes) if a.all else a.screen
    if not screens:
        sys.exit("pass --screen NAME or --all")
    failed = [s for s in screens if run(a.base, s, routes, pathlib.Path(a.out))]
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
