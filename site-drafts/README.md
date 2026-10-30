# site-drafts: legal pages and email sign-up, not deployed yet

This folder sits outside `site/`, so nothing here goes live. `.github/workflows/pages.yml` publishes only `./site`.

| File | What it is |
|---|---|
| `impressum.html` | Impressum (legal notice) per § 5 DDG, with placeholders |
| `privacy.html` | Datenschutzerklärung (privacy policy) per GDPR, with placeholders |
| `PROVIDERS.md` | MailerLite vs Buttondown vs Kit, the recommendation, and the exact clicks to get the form action URL |

These are templates written for a German site owner. They are not legal advice. If you are unsure, have them checked (for example with a generator such as e-recht24 or a lawyer).

## When does the sign-up box appear?

`site/signup.js` shows the newsletter / Mac app waitlist box only when **both** are true:

1. `signupFormAction` in `site/site-config.js` is set to the provider's form action URL (`https://...`), and
2. `https://agent-deck.com/privacy.html` is live (returns 200).

If either is missing, the page shows nothing: there is never a dead form or a form without a privacy policy. The page that should show the box needs `<div id="signup-slot"></div>` and `<script src="/signup.js" defer></script>`; `signup.js` loads `site-config.js` by itself.

## Owner details needed (never invented; fill them in yourself)

| Placeholder | What to put | Used in |
|---|---|---|
| `{{OWNER_FULL_NAME}}` | Your full legal name (or company name and legal form) | both |
| `{{STREET_ADDRESS}}` | Street and house number where you can be served with letters (a P.O. box is not enough) | both |
| `{{POSTCODE_CITY}}` | Postcode and city | both |
| `{{COUNTRY}}` | Country | both |
| `{{CONTACT_EMAIL}}` | An email address you read | both |
| `{{PHONE_OPTIONAL}}` | Phone number, or delete the line (an email you answer promptly is usually enough) | impressum |
| `{{VAT_ID_IF_ANY}}` | USt-IdNr. if you have one; otherwise delete the whole "Umsatzsteuer-ID" section | impressum |
| `{{NEWSLETTER_PROVIDER_NAME_AND_ADDRESS}}` | Legal name and postal address of the provider you chose (from its privacy policy) | privacy |
| `{{NEWSLETTER_PROVIDER_PRIVACY_URL}}` | Link to that provider's privacy policy | privacy |
| `{{NEWSLETTER_TRACKING_NOTE_OPTIONAL}}` | e.g. "We have switched off open and click tracking." or delete | privacy |
| `{{SUPERVISORY_AUTHORITY_OPTIONAL}}` | Optional: name and website of your state's data protection authority (Landesdatenschutzbehörde), or delete | privacy |
| `{{LAST_UPDATED_DATE}}` | The date you publish the policy | privacy |

Also in `privacy.html`, delete the blocks between `<!-- IF ... -->` and `<!-- END IF ... -->` that do not apply:

- "IF outside EU": delete for MailerLite (EU data), keep for Buttondown or Kit (USA).
- "IF Google Fonts": keep while `site/index.html` loads fonts from fonts.googleapis.com (it does today). Better: self-host the two fonts and delete this block; German courts have fined sites for loading Google Fonts remotely (LG München I, 20.01.2022, 3 O 17493/20).
- "IF PostHog": keep while `site/index.html` has the PostHog page-view snippet (it does today).

Check: `grep -n '{{' site-drafts/*.html` must print nothing before you move the files.

## How to go live

1. Pick a provider (see `PROVIDERS.md`), create the form, switch double opt-in **on**.
2. Fill every placeholder above in `impressum.html` and `privacy.html`; delete the IF blocks that do not apply.
3. Move both files into `site/`:
   ```bash
   git mv site-drafts/impressum.html site/impressum.html
   git mv site-drafts/privacy.html site/privacy.html
   ```
4. Add both to `site/sitemap.xml`:
   ```xml
   <url><loc>https://agent-deck.com/impressum.html</loc><lastmod>YYYY-MM-DD</lastmod></url>
   <url><loc>https://agent-deck.com/privacy.html</loc><lastmod>YYYY-MM-DD</lastmod></url>
   ```
5. Link both from the footer of every page (German law wants the Impressum reachable in one click from every page): `<a href="/impressum.html">Impressum</a> · <a href="/privacy.html">Privacy</a>`.
6. Set `signupFormAction` in `site/site-config.js` to the form action URL.
7. Push to `main`. After the Pages deploy, open https://agent-deck.com/privacy.html (must load), then the home page: the sign-up box appears. Sign up once yourself and confirm that the confirmation email arrives.

Order matters only for the box: it stays hidden until step 3 (privacy page live) and step 6 (URL set) are both done.
