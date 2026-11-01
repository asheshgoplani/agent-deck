/*
 * Email sign-up box for agent-deck.com (Mac app beta testers + occasional updates).
 *
 * Usage in a page:  <div id="signup-slot"></div>
 *                   <p id="signup-fallback">...</p>          (optional, hidden when the form shows)
 *                   <a id="privacy-link" href="/privacy.html" hidden>Privacy</a>   (optional)
 *                   <script src="/signup.js" defer></script>
 *
 * This script loads /site-config.js itself, then renders the box ONLY when
 *   1. AGENT_DECK_SITE.signupFormAction is a non-empty https URL, and
 *   2. /privacy.html answers 200 (the privacy policy is live), and
 *   3. the page has an element with id="signup-slot".
 * Otherwise it adds nothing to the page and the fallback text stays.
 * Separately, it unhides #privacy-link whenever /privacy.html answers 200.
 *
 * The box is a plain HTML <form method="post"> to the provider. No provider
 * script is loaded, and this script sets no cookies and uses no storage.
 * Styles come from /site.css (classes .su, .check, .consent, .err, .ok).
 */
(function () {
    'use strict';

    var thisScript = document.currentScript;
    var base = (thisScript && thisScript.src) ? thisScript.src : window.location.href;

    function siteUrl(path) {
        return new URL(path, base).href;
    }

    function loadConfig(done) {
        if (window.AGENT_DECK_SITE) { done(); return; }
        var s = document.createElement('script');
        s.src = siteUrl('site-config.js');
        s.onload = done;
        s.onerror = function () { /* no config: render nothing */ };
        document.head.appendChild(s);
    }

    // Which provider the action URL belongs to, and how it wants its fields.
    function detectProvider(actionUrl) {
        var host = actionUrl.hostname.toLowerCase();
        function is(domain) { return host === domain || host.slice(-(domain.length + 1)) === '.' + domain; }

        if (is('buttondown.com') || is('buttondown.email')) {
            return { id: 'buttondown', name: 'Buttondown', emailField: 'email', target: 'blank' };
        }
        if (host === 'app.kit.com' || host === 'app.convertkit.com') {
            return { id: 'kit', name: 'Kit', emailField: 'email_address', target: 'blank' };
        }
        if (host === 'assets.mailerlite.com' || host === 'app.mailerlite.com' || host === 'landing.mailerlite.com') {
            // MailerLite's form endpoint answers with JSON, so post into a hidden
            // frame and show our own "check your inbox" message instead.
            return {
                id: 'mailerlite', name: 'MailerLite', emailField: 'fields[email]', target: 'frame',
                hidden: { 'ml-submit': '1', 'anticsrf': 'true' }
            };
        }
        return { id: 'other', name: 'our email provider', emailField: 'email', target: 'blank' };
    }

    // How the two checkboxes are sent. Returns { name, macApp, newsletter }.
    // name is null when the provider cannot receive the choice (Kit/MailerLite
    // without ids, or an unknown provider). run() then renders no form, so a
    // visitor never ticks a box that is ignored. Set signupInterestIds.
    function interestFields(provider, ids) {
        ids = ids || {};
        var newsletterId = String(ids.newsletter || '').trim();
        var macId = String(ids.macApp || '').trim();
        if (provider.id === 'buttondown') {
            return { name: 'tag', macApp: 'mac-app-beta', newsletter: 'newsletter' };
        }
        if (provider.id === 'kit' && newsletterId && macId) {
            return { name: 'tags[]', macApp: macId, newsletter: newsletterId };
        }
        if (provider.id === 'mailerlite' && newsletterId && macId) {
            return { name: 'groups[]', macApp: macId, newsletter: newsletterId };
        }
        return { name: null, macApp: 'mac-app-beta', newsletter: 'newsletter' };
    }

    function el(tag, attrs, children) {
        var node = document.createElement(tag);
        if (attrs) {
            Object.keys(attrs).forEach(function (k) {
                if (k === 'class') node.className = attrs[k];
                else if (k === 'text') node.textContent = attrs[k];
                else node.setAttribute(k, attrs[k]);
            });
        }
        (children || []).forEach(function (c) {
            node.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
        });
        return node;
    }

    // A checkbox that is never pre-ticked.
    function checkbox(id, name, value, title, detail) {
        var attrs = { type: 'checkbox', id: id, value: value };
        if (name) attrs.name = name;
        var input = el('input', attrs);
        var wrap = el('label', { class: 'check', for: id }, [
            input,
            el('span', null, [el('strong', { text: title }), el('span', { text: detail })])
        ]);
        return { wrap: wrap, input: input };
    }

    function render(slot, cfg, actionUrl) {
        var provider = detectProvider(actionUrl);
        var interests = interestFields(provider, cfg.signupInterestIds);
        var uid = 'ad-signup';

        var form = el('form', {
            method: 'post',
            action: actionUrl.href,
            'accept-charset': 'UTF-8',
            class: 'su',
            novalidate: 'novalidate',
            'aria-label': 'Sign up for the Mac app beta or updates'
        });

        if (provider.target === 'frame') {
            var frameName = uid + '-sink';
            var frame = el('iframe', { name: frameName, title: 'Sign-up response', 'aria-hidden': 'true', tabindex: '-1', hidden: 'hidden' });
            slot.appendChild(frame);
            form.setAttribute('target', frameName);
        } else {
            form.setAttribute('target', '_blank');
        }

        // Email
        var emailInput = el('input', {
            type: 'email', name: provider.emailField, id: uid + '-email', required: 'required',
            autocomplete: 'email', placeholder: 'you@example.com', inputmode: 'email'
        });
        form.appendChild(el('div', null, [
            el('label', { for: uid + '-email', class: 'field', text: 'Email' }),
            emailInput
        ]));

        // Interests: neither box is pre-ticked; at least one is required.
        var mac = checkbox(uid + '-mac', interests.name, interests.macApp,
            'Mac app beta tester', 'I want to test it and give feedback');
        var news = checkbox(uid + '-news', interests.name, interests.newsletter,
            'Occasional updates', 'Release notes, a few times a year');
        var boxes = [mac.input, news.input];
        form.appendChild(el('fieldset', null, [
            el('legend', { text: 'What would you like? Pick at least one.' }),
            mac.wrap, news.wrap
        ]));

        // Provider-specific hidden fields
        Object.keys(provider.hidden || {}).forEach(function (k) {
            form.appendChild(el('input', { type: 'hidden', name: k, value: provider.hidden[k] }));
        });

        // Consent line, right above the button.
        form.appendChild(el('p', { class: 'consent', id: uid + '-consent' }, [
            'By signing up you agree that Agent Deck emails you about what you ticked. ' +
            'You get a confirmation email first (double opt-in). Unsubscribe any time. ' +
            'Your address is stored by ' + provider.name + '. ',
            el('a', { href: siteUrl('privacy.html'), text: 'Privacy policy' }), '.'
        ]));

        var button = el('button', { type: 'submit', 'aria-describedby': uid + '-consent', text: 'Sign up' });
        var errorBox = el('p', { class: 'err', role: 'alert' });
        var doneBox = el('p', { class: 'ok', role: 'status' });
        form.appendChild(button);
        form.appendChild(errorBox);
        form.appendChild(doneBox);

        form.addEventListener('submit', function (ev) {
            var problem = '';
            if (!emailInput.value.trim() || !emailInput.checkValidity()) problem = 'Please enter a valid email address.';
            else if (!boxes.some(function (b) { return b.checked; })) problem = 'Please tick at least one: Mac app beta tester or Occasional updates.';
            if (problem) {
                ev.preventDefault();
                errorBox.textContent = problem;
                doneBox.textContent = '';
                return;
            }
            errorBox.textContent = '';
            doneBox.textContent = provider.target === 'frame'
                ? 'Almost done: check your inbox and click the link in the confirmation email.'
                : 'Almost done: finish in the new tab if asked, then click the link in the confirmation email.';
        });

        slot.appendChild(form);
        var fallback = document.getElementById('signup-fallback');
        if (fallback) fallback.hidden = true;
    }

    var privacyCheck = null;
    function privacyIsLive() {
        if (privacyCheck) return privacyCheck;
        if (!window.fetch) return Promise.resolve(false);
        var url = siteUrl('privacy.html');
        function ok(r) { return !!r && r.status === 200; }
        privacyCheck = fetch(url, { method: 'HEAD', cache: 'no-store', credentials: 'omit' })
            .then(function (r) {
                if (ok(r)) return true;
                if (r.status === 405 || r.status === 501) {
                    return fetch(url, { method: 'GET', cache: 'no-store', credentials: 'omit' }).then(ok);
                }
                return false;
            })
            .catch(function () { return false; });
        return privacyCheck;
    }

    function showPrivacyLink() {
        var link = document.getElementById('privacy-link');
        if (!link) return;
        privacyIsLive().then(function (live) { if (live) link.hidden = false; });
    }

    function run() {
        var cfg = window.AGENT_DECK_SITE || {};
        var raw = String(cfg.signupFormAction || '').trim();
        if (!raw) return;                       // not configured: render nothing

        var actionUrl;
        try { actionUrl = new URL(raw); } catch (e) { return; }
        if (actionUrl.protocol !== 'https:') return;

        var slot = document.getElementById('signup-slot');
        if (!slot || !window.fetch) return;

        // The visitor's checkbox choice must reach the provider. If it cannot
        // (MailerLite/Kit without signupInterestIds, or an unknown provider),
        // render nothing rather than checkboxes that are silently ignored.
        if (!interestFields(detectProvider(actionUrl), cfg.signupInterestIds).name) return;

        privacyIsLive().then(function (live) {
            if (!live) return;                  // no privacy page: render nothing
            render(slot, cfg, actionUrl);
        });
    }

    function start() {
        showPrivacyLink();                      // independent of the form config
        loadConfig(run);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', start);
    } else {
        start();
    }
})();
