# Open a page on the user's phone and Mac app

One command shows a file you wrote on both screens the user watches:

```bash
agent-deck open report.html --session "$AGENTDECK_INSTANCE_ID"
agent-deck open notes.md   --session "$AGENTDECK_INSTANCE_ID" --json   # Markdown is fine
```

- The Mac app shows it in this session's Browser panel.
- The phone app shows it in this session's Pages panel; it opens by itself when the user is on this
  session, otherwise a quiet "New page" banner appears.
- `--session` defaults to `$AGENTDECK_INSTANCE_ID`, so inside an agent-deck session it can be left out.

## When to use it

- The user asks to see, open or show something you wrote: a plan, a comparison table, a mockup, a
  report, a diagram, brainstorm notes.
- The answer is long or visual (tables, charts, side-by-side options): write a page and open it,
  then say one line in chat ("Opened plan.html on your phone").
- After a follow-up edit, just save the file again: the user taps Refresh on the phone and sees the
  new version. Open it again only if you want it to jump to the top of the list.

## Write pages that read well on a phone

- One self-contained file is best. Relative assets (images, CSS, JS next to the page) are fine:
  they are bundled into the page for the phone. Files outside the session's project are not.
- HTML: include `<meta name="viewport" content="width=device-width, initial-scale=1">`, use
  readable font sizes, and let wide tables scroll (`overflow-x:auto` on a wrapper). Inline `<script>`
  works (charts); the page cannot call agent-deck or read cookies.
- Markdown: headings, lists, tables, code blocks, links and images are rendered to clean HTML with
  light and dark themes. Raw HTML inside Markdown is shown as text.

## Where the file must live

Only files under this session's project path, or under a `[web] file_link_roots` entry, reach the
phone (the same rules as `agent-deck file read`). A file anywhere else still opens in the Mac app but
`--json` reports `"where":"mac","phone":"outside_roots"`; move it into the project to show it on the
phone. Never put secrets in a page.

## Result (`--json`)

```json
{"status":"opened|queued","where":"mac|phone|both","session":"<id>","title":"<session title>",
 "path":"/abs/file","url":"","page_id":"…","kind":"html|markdown|text|image|url","phone":"shown|outside_roots|url","elapsed_ms":42}
```

`status` is `opened` when the Mac app confirmed it, `queued` when it opens once the app connects.

## Related commands

| Command | Does |
|---|---|
| `agent-deck open --last --session <id> [--json]` | re-open the newest page of a session (exit 2 when it has none) |
| `agent-deck open --list --session <id> --json` | the session's pages, newest first |
| `agent-deck open --show <page-id> --session <id> [--json]` | the page as one self-contained HTML document |

Details: [docs/macapp-open.md](https://github.com/asheshgoplani/agent-deck/blob/main/docs/macapp-open.md) ("On the phone").
