# Provider API References

External service-provider API references kept in this repo for offline use and
LLM context. Each provider has a generated reference under `docs/providers/<name>/`
and reproducible generator scripts under `scripts/providers/<name>/`.

## Providers

| Provider | Product | Auth | Base URL | Scope | Reference |
|---|---|---|---|---|---|
| Cartrack | Fleet API | HTTP Basic | regional `fleetapi-<region>.cartrack.com/rest` | 256 ops · 43 categories · 115 schemas | [cartrack/](cartrack/README.md) |

## Layout convention (per provider)

```
docs/providers/<name>/
  README.md          # provider summary: what it is, auth, base URLs, rate limits, endpoint category index
  endpoints/*.md     # endpoint reference, one file per tag/category (+ _schemas.md if OpenAPI)
  general/*.md       # how-to & conceptual pages (auth, rate limiting, webhooks, quick start, …)
scripts/providers/<name>/   # fetch + generate scripts; spec/sitemap gitignored
```

## Adding a provider

1. Fetch the provider's API spec (OpenAPI / Postman / etc.) into `scripts/providers/<name>/`.
2. Write generator scripts there that emit `docs/providers/<name>/` mirroring the layout above.
3. Add a row to the **Providers** table.
4. Run `scripts/providers/gen_llms_section.py` to refresh the provider section of `llms-full.txt`.

## LLM context

A summary of every provider is appended to [`../../llms-full.txt`](../../llms-full.txt)
under the `# PROVIDER API REFERENCES` marker (summary only — full per-endpoint detail
stays in the files above and is referenced by path). Regenerate with
`scripts/providers/gen_llms_section.py`.
