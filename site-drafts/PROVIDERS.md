# Email sign-up provider: options

Researched on 2026-10-30. Free tiers change often (MailerLite cut its free tier twice in 2025-26), so check the pricing page before you sign up.

The site's sign-up box (`site/signup.js`) works with all three. You set one value, `signupFormAction` in `site/site-config.js`, to the provider's form action URL.

| | **MailerLite** | **Buttondown** | **Kit** (formerly ConvertKit) |
|---|---|---|---|
| Free tier | 250 subscribers, 2,500 emails/month, 3 forms; MailerLite logo stays in emails | 100 subscribers | 10,000 subscribers, unlimited forms; Kit branding stays |
| Double opt-in | Yes, per form (Forms > form > Double opt-in) | Yes, on by default | Yes (per-form "incentive email" confirmation) |
| Company | MailerLite, Vilnius, Lithuania (EU) | Buttondown LLC, Richmond, Virginia, USA | Kit (ConvertKit LLC), USA |
| Data location | EU data center (ISO 27001) | USA | USA, no EU data residency |
| DPA (Art. 28) | Built into the Terms of Use, nothing to sign | Published DPA, subprocessor list | Published DPA (kit.com/dpa), Data Privacy Framework + SCCs |
| Export | CSV export of subscribers | CSV export | CSV export (Subscribers > Bulk actions > Export) |
| Plain HTML form | Yes (embed HTML posts to `assets.mailerlite.com/jsonp/...`) | Yes, designed for it (`embed-subscribe`) | Yes (`app.kit.com/forms/<id>/subscriptions`) |
| Newsletter vs waitlist split | Groups (needs group ids in `signupInterestIds`) | Tags `newsletter` / `mac-app-waitlist`, no ids needed (tagging may be a paid add-on, check) | Tags (needs tag ids in `signupInterestIds`) |

## Recommendation: MailerLite

**Why, in one line:** it is the only one of the three that is an EU company storing data in the EU with the DPA already in its terms, which is the cleanest GDPR position for a German site owner.

Trade-offs: the free tier is small (250 subscribers), and free emails carry the MailerLite logo. If the list grows past 250, move to a paid MailerLite plan (check current price), or switch to Kit (10,000 free, but US data with SCCs). Pick Buttondown if you want the simplest, most developer-friendly setup and accept US hosting and a 100-subscriber free tier.

## How to get the form action URL (MailerLite)

1. Sign up at https://www.mailerlite.com and finish the account setup (MailerLite may ask a few questions to approve a new account).
2. Left menu: **Subscribers** > **Groups** > **Create group**. Make one called `Newsletter` and one called `Mac app waitlist`.
3. Left menu: **Forms** > **Embedded forms** tab > **Create form**.
4. Name it `agent-deck.com`, choose **Embedded form**, click **Save and continue**.
5. Tick both groups (`Newsletter` and `Mac app waitlist`), click **Continue**, then **Continue** again.
6. In the editor, leave just the Email field. Click **Next** / **Save**.
7. Open the form again and go to its **Double opt-in** tab: switch it **on**, and edit the confirmation email text if you like. Save.
8. **Forms** > **Embedded forms** > click **Overview** on the form.
9. Scroll to **Embed form to your website**, click the **HTML** tab.
10. In the code, find `<form ... action="https://assets.mailerlite.com/jsonp/.../forms/.../subscribe" ...>`. Copy only the URL inside `action="..."`.
11. Paste it into `site/site-config.js`: `signupFormAction: "https://assets.mailerlite.com/jsonp/.../subscribe"`.
12. Optional, to let people choose newsletter vs waitlist: in **Subscribers** > **Groups**, open each group and copy its group id (shown in the group's page address; if not, ask MailerLite support or use Integrations > API). Put them in `signupInterestIds: { newsletter: "<id>", macApp: "<id>" }`. Without ids, everyone goes to both groups ticked in step 5.
13. Get the DPA for your records: it is part of MailerLite's Terms; a copy is at https://www.mailerlite.com/legal/data-processing-agreement.

For the privacy page, `{{NEWSLETTER_PROVIDER_NAME_AND_ADDRESS}}` is MailerLite's legal entity and address as given in its privacy policy (https://www.mailerlite.com/legal/privacy-policy); copy it from there, and delete the "IF outside EU" paragraph.

## Sources

- MailerLite pricing (free plan limits, EU data center, GDPR FAQ): https://www.mailerlite.com/pricing
- MailerLite DPA: https://www.mailerlite.com/legal/data-processing-agreement
- MailerLite GDPR page (DPA part of Terms): https://www.mailerlite.com/gdpr-compliance
- MailerLite double opt-in: https://www.mailerlite.com/help/how-to-use-double-opt-in-when-collecting-subscribers
- MailerLite embedded form and HTML code: https://www.mailerlite.com/help/how-to-create-an-embedded-form
- MailerLite export: https://www.mailerlite.com/help/how-to-export-subscribers
- MailerLite free plan change (2026): https://mailsoftly.com/blog/mailerlite-free-plan-changes/
- Buttondown pricing: https://buttondown.com/pricing
- Buttondown double opt-in: https://docs.buttondown.com/double-opt-in
- Buttondown DPA: https://buttondown.com/legal/data-processing-agreement
- Buttondown subprocessors: https://buttondown.com/legal/subprocessors
- Buttondown GDPR page: https://buttondown.com/legal/gdpr-eu-compliance
- Buttondown embedding (form action, `email`, `tag` fields): https://docs.buttondown.com/building-your-subscriber-base and https://docs.buttondown.com/shadcn-ui
- Kit pricing: https://kit.com/pricing
- Kit GDPR (DPF, SCCs, DPA): https://kit.com/gdpr
- Kit export: https://help.kit.com/articles/2502489-how-to-export-subscribers
- Kit data location (secondary source): https://themailstack.pages.dev/tools/kit/
