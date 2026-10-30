/*
 * Site settings for agent-deck.com. signup.js loads this file by itself;
 * pages only need <script src="/signup.js" defer></script> and an empty
 * <div id="signup-slot"></div> where the sign-up box should appear.
 *
 * signupFormAction  <- THE ONE VALUE TO SET.
 *   The "action" URL of your email provider's subscribe form, copied from the
 *   provider's embed / HTML form code. Examples of the shape (not real):
 *     MailerLite: https://assets.mailerlite.com/jsonp/<account-id>/forms/<form-id>/subscribe
 *     Buttondown: https://buttondown.com/api/emails/embed-subscribe/<username>
 *     Kit:        https://app.kit.com/forms/<form-id>/subscriptions
 *   While it is "" (empty), the sign-up box is not shown at all.
 *   It also stays hidden until /privacy.html is live on the site, so there is
 *   never a form without a privacy policy. Turn on double opt-in in the provider.
 *   See site-drafts/README.md and site-drafts/PROVIDERS.md in the repository.
 *
 * signupInterestIds  (optional, leave as is if unsure)
 *   Kit and MailerLite need numeric ids to file people under "Newsletter" or
 *   "Mac app waitlist" (Kit: tag ids, MailerLite: group ids). Without ids the
 *   box shows no interest choice and everyone joins the one list. Buttondown
 *   needs no ids: it receives the tags "newsletter" / "mac-app-waitlist".
 */
window.AGENT_DECK_SITE = {
    signupFormAction: "",
    signupInterestIds: { newsletter: "", macApp: "" }
};
