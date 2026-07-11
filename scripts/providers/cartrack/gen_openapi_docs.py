#!/usr/bin/env python3
"""Generate per-tag markdown reference docs from the Cartrack Fleet API OpenAPI spec."""
import os, re, json, yaml
from collections import defaultdict

HERE = os.path.dirname(os.path.abspath(__file__))
def _repo_root():
    d = HERE
    while d != os.path.dirname(d):
        if os.path.exists(os.path.join(d, "go.mod")): return d
        d = os.path.dirname(d)
    raise SystemExit("repo root (go.mod) not found above " + HERE)
REPO = _repo_root()
DEST = os.path.join(REPO, "docs", "providers", "cartrack", "endpoints")
SPEC_FILE = os.path.join(HERE, "openapi.yaml")
SPEC_URL = "https://developer.cartrack.com/openapi/openapi.yaml"
METHODS = ("get", "post", "put", "patch", "delete", "head", "options")

spec = yaml.safe_load(open(SPEC_FILE))
comp = spec.get("components", {})
schemas = comp.get("schemas", {})
L = []  # lines buffer

def ref_name(ref):
    return ref.split("/")[-1] if isinstance(ref, str) else None

def resolve(ref):
    """Walk a $ref like '#/components/parameters/Foo' through the spec."""
    if not ref or not isinstance(ref, str): return None
    if ref.startswith("#/"):
        node = spec
        for part in ref[2:].split("/"):
            part = part.replace("~1", "/").replace("~0", "~")
            if isinstance(node, dict) and part in node:
                node = node[part]
            else:
                return None
        return node
    return None

def slugify(s):
    return re.sub(r"[^a-z0-9]+", "-", (s or "").lower()).strip("-")

def schema_type(schema):
    if not schema: return ""
    if "$ref" in schema:
        return f"`{ref_name(schema['$ref'])}`"
    t = schema.get("type")
    if isinstance(t, list):
        return " | ".join(t)
    if t == "array" and "items" in schema:
        inner = schema_type(schema["items"])
        return f"array of {inner}"
    if t == "object" and schema.get("properties") and not schema.get("title"):
        return "object"
    return t or ""

def render_schema_table(schema, depth=0, seen=None):
    """Render an object schema's properties as a markdown table."""
    if seen is None: seen = set()
    if not schema: return ""
    if "$ref" in schema:
        name = ref_name(schema["$ref"])
        if name in seen or depth > 3:
            return f"_See [`{name}`](#schema-{slugify(name)})_"
        seen = seen | {name}
        schema = resolve(schema["$ref"]) or {}
    props = schema.get("properties") or {}
    if not props:
        return ""
    required = set(schema.get("required") or [])
    rows = []
    for pname, pschema in props.items():
        ptype = schema_type(pschema)
        desc = (pschema.get("description") or "").strip().replace("\n", " ")
        ex = pschema.get("example")
        ex_s = ""
        if ex is not None:
            ex_s = json.dumps(ex, ensure_ascii=False)
        req = "**required**" if pname in required else ""
        rows.append(f"| `{pname}` | {ptype} | {req} | {desc} | {ex_s} |")
    out = ["| Field | Type | Required | Description | Example |",
           "|---|---|---|---|---|"]
    out.extend(rows)
    return "\n".join(out)

def render_example_block(label, content_type, value):
    if value is None: return ""
    if isinstance(value, (dict, list)):
        body = json.dumps(value, indent=2, ensure_ascii=False)
    else:
        body = str(value)
    lang = "json" if "json" in (content_type or "") else "text"
    return f"**{label}:**\n\n```{lang}\n{body}\n```\n"

def resolve_param(p):
    if isinstance(p, dict) and "$ref" in p:
        return resolve(p["$ref"]) or p
    return p

def render_params(params, location):
    params = [resolve_param(p) for p in (params or [])]
    rows = [p for p in params if p.get("in") == location]
    if not rows: return ""
    out = ["| Name | Required | Type | Description | Example |",
           "|---|---|---|---|---|"]
    for p in rows:
        t = schema_type(p.get("schema") or {})
        desc = (p.get("description") or "").strip().replace("\n", " ")
        ex = p.get("example")
        ex_s = json.dumps(ex, ensure_ascii=False) if ex is not None else (p.get("schema",{}) or {}).get("example","")
        if not ex_s and p.get("schema"):
            ex_s = json.dumps(p["schema"].get("example",""), ensure_ascii=False) if p["schema"].get("example") is not None else ""
        req = "**required**" if p.get("required") else "optional"
        out.append(f"| `{p['name']}` | {req} | {t} | {desc} | {ex_s} |")
    return "\n".join(out) + "\n"

def render_request_body(rb):
    if not rb: return "_No request body._\n"
    out = []
    desc = rb.get("description")
    if desc: out.append(desc + "\n")
    content = rb.get("content") or {}
    for ct, media in content.items():
        out.append(f"**Content-Type:** `{ct}`\n")
        schema = media.get("schema")
        if schema:
            if "$ref" in schema or (schema.get("type") == "object" and schema.get("properties")):
                out.append("\n" + render_schema_table(schema) + "\n")
            else:
                out.append(f"\n_Schema: {schema_type(schema)}_\n")
        ex = media.get("example")
        if ex is not None:
            out.append(render_example_block("Example", ct, ex))
        for ename, eval_ in (media.get("examples") or {}).items():
            val = eval_.get("value") if isinstance(eval_, dict) else eval_
            out.append(render_example_block(f"Example ({ename})", ct, val))
    return "\n".join(out)

def render_responses(responses):
    out = []
    for code, resp in sorted(responses.items()):
        if isinstance(resp, dict) and "$ref" in resp:
            resp = resolve(resp["$ref"]) or {}
        desc = resp.get("description") or ""
        out.append(f"#### `{code}` — {desc}\n")
        content = resp.get("content") or {}
        for ct, media in content.items():
            out.append(f"**Content-Type:** `{ct}`\n")
            schema = media.get("schema")
            if schema and ("$ref" in schema or schema.get("type") == "object"):
                out.append("\n" + render_schema_table(schema) + "\n")
            ex = media.get("example")
            if ex is not None:
                out.append(render_example_block("Example", ct, ex))
    return "\n".join(out)

def render_operation(op, path, method):
    L = []
    summary = op.get("summary") or ""
    desc = op.get("description") or ""
    opid = op.get("operationId") or ""
    L.append(f"## {method.upper()} `{path}`")
    if summary: L.append(f"\n**{summary}**\n")
    if opid: L.append(f"`operationId: {opid}`\n")
    if desc: L.append(desc + "\n")
    params = op.get("parameters")
    path_p = render_params(params, "path")
    query_p = render_params(params, "query")
    head_p = render_params(params, "header")
    if path_p: L.append("### Path Parameters\n\n" + path_p)
    if query_p: L.append("### Query Parameters\n\n" + query_p)
    if head_p: L.append("### Header Parameters\n\n" + head_p)
    L.append("### Request Body\n\n" + render_request_body(op.get("requestBody")))
    L.append("### Responses\n\n" + render_responses(op.get("responses") or {}))
    return "\n".join(L)

# group operations by tag
by_tag = defaultdict(list)
for path, item in spec.get("paths", {}).items():
    for m in METHODS:
        if m in item:
            op = item[m]
            tags = op.get("tags") or ["Untagged"]
            for t in tags:
                by_tag[t].append((m, path, op))

os.makedirs(DEST, exist_ok=True)
generated = []
for tag in sorted(by_tag):
    ops = by_tag[tag]
    fname = slugify(tag) + ".md"
    fpath = os.path.join(DEST, fname)
    L = []
    L.append("---")
    L.append(f"source: {SPEC_URL}")
    L.append(f"tag: {tag}")
    L.append(f"spec_version: {spec['info']['version']}")
    L.append("---")
    L.append("")
    L.append(f"# {tag}")
    L.append("")
    L.append(f"_{len(ops)} operation(s). Generated from the [Cartrack Fleet API OpenAPI spec]({SPEC_URL}) v{spec['info']['version']}._")
    L.append("")
    # quick TOC
    for m, path, op in ops:
        anchor = slugify(f"{m}-{path}")
        L.append(f"- [{m.upper()} `{path}`](#{anchor}) — {op.get('summary','')}")
    L.append("")
    for m, path, op in ops:
        L.append(render_operation(op, path, m))
        L.append("\n---\n")
    with open(fpath, "w") as f:
        f.write("\n".join(L) + "\n")
    generated.append((tag, fname, len(ops)))

# emit a schemas reference file
schema_path = os.path.join(DEST, "_schemas.md")
L = []
L.append("---")
L.append(f"source: {SPEC_URL}")
L.append("---")
L.append("")
L.append("# Shared Schemas")
L.append("")
L.append(f"_Reusable models referenced across endpoints. Generated from the OpenAPI spec v{spec['info']['version']}._")
L.append("")
for name in sorted(schemas):
    s = schemas[name]
    L.append(f"\n## `{name}` {{#schema-{slugify(name)}}}\n")
    if s.get("description"):
        L.append(s["description"] + "\n")
    L.append(f"_Type: {schema_type(s)}_\n")
    tbl = render_schema_table(s, seen=set())
    if tbl:
        L.append(tbl)
    ex = s.get("example")
    if ex is not None:
        L.append("\n" + render_example_block("Example", "json", ex))
with open(schema_path, "w") as f:
    f.write("\n".join(L) + "\n")

print(f"Generated {len(generated)} tag files + 1 schemas file in {DEST}")
for tag, fname, n in generated:
    print(f"  {fname:40s} {n:3d} ops")
print(f"Total operations: {sum(n for _,_,n in generated)}")
