/*
 * Site settings for agent-deck.com. signup.js loads this file by itself;
 * pages only need <script src="/signup.js" defer></script> and an empty
 * <div id="signup-slot"></div> where the sign-up box should appear.
 *
 * signupFormAction  <- THE ONE VALUE TO SET. "" means OFF.
 *   The "action" URL of your email provider's subscribe form, copied from the
 *   provider's embed / HTML form code. Shape of the URL (not real):
 *     MailerLite (recommended, see site-drafts/PROVIDERS.md):
 *       https://assets.mailerlite.com/jsonp/<account-id>/forms/<form-id>/subscribe
 *     Buttondown: https://buttondown.com/api/emails/embed-subscribe/<username>
 *     Kit:        https://app.kit.com/forms/<form-id>/subscriptions
 *   While it is "" (empty), the sign-up box is not shown at all and the page
 *   shows "Sign-up opens soon ... Discord" instead.
 *   It also stays hidden until /privacy.html is live on the site, so there is
 *   never a form without a privacy policy. Turn on double opt-in in the provider.
 *   See site-drafts/README.md and site-drafts/PROVIDERS.md in the repository.
 *
 * signupInterestIds  (SET THESE for MailerLite or Kit)
 *   The form has two checkboxes, neither pre-ticked:
 *     macApp     = "Mac app beta testers" group (I want to test it and give feedback)
 *     newsletter = "Occasional updates" group (release notes, a few times a year)
 *   MailerLite needs the two group ids (sent as groups[]), Kit the two tag ids
 *   (sent as tags[]). Buttondown needs no ids: it gets the tags "mac-app-beta"
 *   and "newsletter".
 *   If the ids are missing for MailerLite or Kit, the form stays hidden (the
 *   visitor's choice could not be sent, so people would get what they did not
 *   tick). Set both ids, then the form shows.
 */
window.AGENT_DECK_SITE = {
    signupFormAction: "",
    signupInterestIds: { macApp: "", newsletter: "" }
};
