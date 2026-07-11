#!/usr/bin/env python3
"""Crawl in-scope Cartrack Fleet API doc pages -> markdown under docs/providers/cartrack/."""
import os, re, sys, time, urllib.request, urllib.error
from concurrent.futures import ThreadPoolExecutor, as_completed
import html2text

HERE = os.path.dirname(os.path.abspath(__file__))
def _repo_root():
    d = HERE
    while d != os.path.dirname(d):
        if os.path.exists(os.path.join(d, "go.mod")): return d
        d = os.path.dirname(d)
    raise SystemExit("repo root (go.mod) not found above " + HERE)
REPO = _repo_root()
DEST = os.path.join(REPO, "docs", "providers", "cartrack")
UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
URLS_FILE = os.path.join(HERE, "sitemap_urls.txt")

# html2text config: no line wrapping, keep code, sane defaults
H = html2text.HTML2Text()
H.body_width = 0
H.ignore_images = False
H.ignore_links = False
H.protect_links = True
H.unicode_snob = True

def in_scope(url):
    """How-to/general pages only. Endpoint detail is generated from openapi.yaml,
    so exclude /docs/fleet-api/* rendered pages (SSR lacks request body)."""
    return (
        url.rstrip("/").endswith("/docs/introduction")
        or "/docs/applications/fleet-api" in url
        or "/docs/fleet-api-general/" in url
    )

def local_path(url):
    """Map source URL -> local .md path mirroring structure."""
    path = url.replace("https://developer.cartrack.com/docs/", "").strip("/")
    # remap fleet-api-general -> general, fleet-api -> endpoints
    if path.startswith("fleet-api-general/"):
        path = "general/" + path[len("fleet-api-general/"):]
    elif path == "fleet-api-general":
        path = "general"
    elif path.startswith("fleet-api/"):
        path = "endpoints/" + path[len("fleet-api/"):]
    elif path == "fleet-api":
        path = "endpoints"
    # applications/* stays, introduction stays
    return os.path.join(DEST, path + ".md")

def fetch(url, tries=3):
    last = None
    for i in range(tries):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": UA})
            with urllib.request.urlopen(req, timeout=30) as r:
                return r.read().decode("utf-8", "replace")
        except Exception as e:
            last = e
            time.sleep(0.5 * (i + 1))
    raise last

def extract_title(html):
    m = re.search(r"<title[^>]*>(.*?)</title>", html, re.S)
    if not m:
        return ""
    t = re.sub(r"\s+", " ", m.group(1)).strip()
    return re.sub(r"\s*\|\s*Cartrack for Developers\s*$", "", t).strip()

def extract_markdown_div(html):
    """Find <div class="theme-doc-markdown markdown"> and match its close via depth."""
    needle = 'class="theme-doc-markdown markdown"'
    start = html.find(needle)
    if start == -1:
        # fallback: article
        m = re.search(r"<article[^>]*>(.*?)</article>", html, re.S)
        return m.group(1) if m else html
    # find the '>' that closes the opening div tag
    tag_close = html.find(">", start)
    inner_start = tag_close + 1
    depth = 1
    i = inner_start
    open_re = re.compile(r"<div\b", re.I)
    close_re = re.compile(r"</div\s*>", re.I)
    while i < len(html) and depth > 0:
        om = open_re.search(html, i)
        cm = close_re.search(html, i)
        if cm is None:
            break
        if om and om.start() < cm.start():
            depth += 1
            i = om.end()
        else:
            depth -= 1
            i = cm.end()
            if depth == 0:
                return html[inner_start:cm.start()]
        i = i  # noop
    return html[inner_start:i]

def convert(url):
    try:
        html = fetch(url)
        title = extract_title(html)
        body = extract_markdown_div(html)
        md = H.handle(body).strip()
        # tidy: collapse 3+ blank lines, strip trailing whitespace per line
        md = re.sub(r"\n{3,}", "\n\n", md)
        md = "\n".join(l.rstrip() for l in md.splitlines())
        out = local_path(url)
        front = f"---\nsource: {url}\ntitle: {title}\n---\n\n# {title}\n\n"
        os.makedirs(os.path.dirname(out), exist_ok=True)
        with open(out, "w") as f:
            f.write(front + md + "\n")
        return (url, out, len(md), None)
    except Exception as e:
        return (url, None, 0, str(e))

def main():
    urls = [l.strip() for l in open(URLS_FILE) if l.strip()]
    urls = [u for u in urls if in_scope(u)]
    print(f"in-scope pages: {len(urls)}", flush=True)
    ok = 0; fail = 0; errors = []
    with ThreadPoolExecutor(max_workers=6) as ex:
        futs = {ex.submit(convert, u): u for u in urls}
        for n, fut in enumerate(as_completed(futs), 1):
            url, out, size, err = fut.result()
            if err:
                fail += 1; errors.append((url, err))
                print(f"[{n}/{len(urls)}] FAIL {url}: {err}", flush=True)
            else:
                ok += 1
                if n % 25 == 0 or n <= 3:
                    print(f"[{n}/{len(urls)}] ok {os.path.relpath(out, DEST)} ({size} chars)", flush=True)
    print(f"\nDONE: ok={ok} fail={fail}", flush=True)
    if errors:
        print("FAILURES:")
        for u, e in errors:
            print(f"  {u}: {e}")

if __name__ == "__main__":
    main()
