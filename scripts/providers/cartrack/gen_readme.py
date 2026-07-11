#!/usr/bin/env python3
"""Generate docs/providers/cartrack/README.md index from the OpenAPI spec."""
import os, re, yaml
from collections import defaultdict

HERE = os.path.dirname(os.path.abspath(__file__))
def _repo_root():
    d = HERE
    while d != os.path.dirname(d):
        if os.path.exists(os.path.join(d, "go.mod")): return d
        d = os.path.dirname(d)
    raise SystemExit("repo root (go.mod) not found above " + HERE)
REPO = _repo_root()
DEST = os.path.join(REPO, "docs", "providers", "cartrack")
SPEC_FILE = os.path.join(HERE, "openapi.yaml")
SPEC_URL = "https://developer.cartrack.com/openapi/openapi.yaml"
SITE = "https://developer.cartrack.com/docs/fleet-api-general/overview"
METHODS = ("get","post","put","patch","delete","head","options")
spec = yaml.safe_load(open(SPEC_FILE))

def slugify(s): return re.sub(r"[^a-z0-9]+","-",(s or "").lower()).strip("-")

servers = spec.get("servers", [])
info = spec["info"]
by_tag = defaultdict(int)
for path, item in spec.get("paths",{}).items():
    for m in METHODS:
        if m in item:
            for t in (item[m].get("tags") or ["Untagged"]):
                by_tag[t]+=1
total_ops = sum(by_tag.values())

L = []
L.append("---")
L.append(f"title: Cartrack Fleet API — Reference")
L.append(f"source_spec: {SPEC_URL}")
L.append(f"spec_version: {info['version']}")
L.append("generated: 2026-07-11")
L.append("---")
L.append("")
L.append("# Cartrack Fleet API — Reference")
L.append("")
L.append(f"> {info['description'].strip().splitlines()[0]}")
L.append("")
L.append(f"This is an offline reference for the **Cartrack Fleet API**, generated from the "
         f"canonical [OpenAPI 3.1 spec]({SPEC_URL}) (v{info['version']}) and the "
         f"[developer docs]({SITE}). It covers **{len(by_tag)} endpoint categories** "
         f"and **{total_ops} operations**.")
L.append("")
L.append("## Why this exists")
L.append("")
L.append("The live endpoint pages render request/response schemas client-side (Redocly OpenAPI plugin), "
         "so the server HTML omits the request body and parameters. This reference is built directly from "
         "the published OpenAPI spec so the detail is complete and accurate offline.")
L.append("")
L.append("## Getting started")
L.append("")
L.append("1. **Authentication** — HTTP Basic Auth. Send an `Authorization: Basic <base64(username:password)>` header "
         "over HTTPS. See [Authentication](general/authentication.md) for admin vs. user credential generation.")
L.append("2. **Base URL** — pick the regional endpoint matching your Cartrack account. See the table below; "
         "full notes in [Base URLs](general/base-url.md).")
L.append("3. **Rate limits** — default **1,000 requests/minute**; some endpoints are stricter (10/min). "
         "Exceeding returns `429`. See [Rate Limiting](general/rate-limiting.md).")
L.append("4. **First request** — [Quick Start](general/quick-start.md).")
L.append("5. **Async updates** — [Webhook Notifications](general/webhook-notification.md).")
L.append("")
L.append("### Regional base URLs")
L.append("")
L.append("| Region | Base URL |")
L.append("|---|---|")
for s in servers:
    L.append(f"| {s.get('description','')} | `{s['url']}` |")
L.append("")
L.append("All paths in the endpoint reference are appended to your regional base URL (e.g. "
         "`https://fleetapi-za.cartrack.com/rest` + `/alerts/ignition`).")
L.append("")
L.append("## How-to & concepts")
L.append("")
L.append("- [Overview](general/overview.md)")
L.append("- [Use Cases](general/use-cases.md)")
L.append("- [Authentication](general/authentication.md)")
L.append("- [Base URLs](general/base-url.md)")
L.append("- [Quick Start](general/quick-start.md)")
L.append("- [Rate Limiting](general/rate-limiting.md)")
L.append("- [Webhook Notification](general/webhook-notification.md)")
L.append("- [Application registration](applications/fleet-api.md)")
L.append("- [Changelog](general/changelog.md)")
L.append("- Service category overviews: [delivery jobs](general/services/delivery-job-services.md), "
         "[driver identification](general/services/driver-identification-services.md), "
         "[fuel](general/services/fuel-services.md), [mileage/odometer](general/services/mileage-odometer-services.md), "
         "[positions/route](general/services/positions-route-services.md), [vehicle sensors](general/services/vehicle-sensors-services.md), "
         "[vehicle temperature](general/services/vehicle-temperature-services.md), "
         "[vehicles/events](general/services/vehicles-events-services.md), [vision](general/services/vision-services.md)")
L.append("")
L.append("## Endpoint reference")
L.append("")
L.append("One file per tag. Each documents every operation with method, path, parameters, request body, "
         "responses, and examples.")
L.append("")
L.append("| Category | Operations | File |")
L.append("|---|---:|---|")
for tag in sorted(by_tag):
    L.append(f"| {tag} | {by_tag[tag]} | [endpoints/{slugify(tag)}.md](endpoints/{slugify(tag)}.md) |")
L.append(f"| **Shared schemas** | — | [endpoints/_schemas.md](endpoints/_schemas.md) |")
L.append("")
L.append("## Scope")
L.append("")
L.append("Includes the **Fleet API** only. Excluded (separate Cartrack products): "
         "Mikey iOS/Android BLE SDKs, mobile SDKs, and telematics data ingestion. "
         "Add them by extending the crawler in `scripts/providers/cartrack/crawl_cartrack.py` if needed.")
L.append("")
L.append("## Regeneration")
L.append("")
L.append("These docs are generated, not hand-maintained. Scripts live in `scripts/providers/cartrack/`.")
L.append("")
L.append("```bash")
L.append("# one-time: tooling (html2text + pyyaml)")
L.append("python3 -m venv .venv && .venv/bin/pip install -q html2text pyyaml")
L.append("")
L.append("# fetch spec + sitemap into scripts/providers/cartrack/")
L.append(f"curl -sL {SPEC_URL} -o scripts/providers/cartrack/openapi.yaml")
L.append("curl -sL https://developer.cartrack.com/sitemap.xml | grep -oE '<loc>[^<]+</loc>' | sed 's/<[^>]*>//g' | grep -E '/docs/' | grep -v '/changelog/' > scripts/providers/cartrack/sitemap_urls.txt")
L.append("")
L.append("# generate (run from repo root)")
L.append(".venv/bin/python scripts/providers/cartrack/crawl_cartrack.py     # how-to pages")
L.append(".venv/bin/python scripts/providers/cartrack/gen_openapi_docs.py   # endpoint docs")
L.append(".venv/bin/python scripts/providers/cartrack/gen_readme.py         # this index")
L.append("```")

out = os.path.join(DEST, "README.md")
with open(out,"w") as f: f.write("\n".join(L)+"\n")
print("wrote", out, f"({len(L)} lines, {total_ops} ops across {len(by_tag)} tags)")
