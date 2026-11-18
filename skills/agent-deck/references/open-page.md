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

- One self-contained file is best. Relative images, CSS and fonts next to the page are bundled
  into it for the phone. Put JavaScript INLINE in `<script>` (a `<script src>` is not loaded), and
  load nothing from the web: the phone page has no network (no CDN scripts, fonts, images or
  fetch). Draw charts with inline SVG or a canvas script.
- HTML: include `<meta name="viewport" content="width=device-width, initial-scale=1">`, use
  readable font sizes, and let wide tables scroll (`overflow-x:auto` on a wrapper).
- Markdown: headings, lists, tables, code blocks, links and images are rendered to clean HTML with
  light and dark themes. Raw HTML inside Markdown is shown as text.

## Where the file must live, and what never reaches the phone

Only files under this session's project path, or under a `[web] file_link_roots` entry, reach the
phone (the same rules as `agent-deck file read`). Dotfiles and dot folders (`.env`, `.git/`, `.ssh/`,
`.aws/` …) and secret-like names (`*secret*`, `*credential*`, `*password*`, `*token*`, `*.pem`,
`*.key`, `id_rsa*` …) never do, not as a page, not as an image or stylesheet inside one, not through
a symlink. Pages are `.html`, `.htm`, `.md`, `.markdown` or `.txt` files only, up to 4 MiB (images,
CSS and fonts are bundled into a page, not opened on their own). Anything else still opens in
the Mac app; `--json` then says `"where":"mac"` and why in `phone` (`outside_roots`, `private`,
`unsupported`, `too_large`). Name the page plainly (`plan.md`, not `secret-plan.md`) and put it in
the project. Never put secrets in a page: it is shown on the phone as written, and a page's own
scripts can read and keep their own cookies and storage (they cannot send anything anywhere: the page
has no network, and every link or navigation out of it needs the owner's tap and confirm).

## Result (`--json`)

```json
{"status":"opened|queued","where":"mac|phone|both","session":"<id>","title":"<session title>",
 "path":"/abs/file","url":"","page_id":"…","page_title":"<the page's own title>","name":"file.html","kind":"html|markdown|text|image|url","phone":"shown|url|outside_roots|private|unsupported|too_large","elapsed_ms":42}
```

`status` is `opened` when the Mac app confirmed it, `queued` when it opens once the app connects.

## Related commands

| Command | Does |
|---|---|
| `agent-deck open --last --session <id> [--json]` | re-open the newest page of a session whose file still exists (exit 2 when none) |
| `agent-deck open --list --session <id> --json` | the session's pages, newest first |
| `agent-deck open --show <page-id> --session <id> [--json]` | the page as one self-contained HTML document |

Details: [docs/macapp-open.md](https://github.com/asheshgoplani/agent-deck/blob/main/docs/macapp-open.md) ("On the phone").
