/*
 * Email sign-up box for agent-deck.com (newsletter + Mac app waitlist).
 *
 * Usage in a page:  <div id="signup-slot"></div>
 *                   <script src="/signup.js" defer></script>
 *
 * This script loads /site-config.js itself, then renders the box ONLY when
 *   1. AGENT_DECK_SITE.signupFormAction is a non-empty https URL, and
 *   2. /privacy.html answers 200 (the privacy policy is live), and
 *   3. the page has an element with id="signup-slot".
 * Otherwise it adds nothing to the page.
 *
 * The box is a plain HTML <form method="post"> to the provider. No provider
 * script is loaded, and this script sets no cookies and uses no storage.
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

    // How the two interests are sent. Returns null when the provider cannot
    // receive them (then the box shows no choice and everyone joins one list).
    function interestFields(provider, ids) {
        ids = ids || {};
        var newsletterId = String(ids.newsletter || '').trim();
        var macId = String(ids.macApp || '').trim();
        if (provider.id === 'buttondown') {
            return { name: 'tag', newsletter: 'newsletter', macApp: 'mac-app-waitlist' };
        }
        if (provider.id === 'kit' && newsletterId && macId) {
            return { name: 'tags[]', newsletter: newsletterId, macApp: macId };
        }
        if (provider.id === 'mailerlite' && newsletterId && macId) {
            return { name: 'groups[]', newsletter: newsletterId, macApp: macId };
        }
        return null;
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

    function checkbox(name, value, label, extraAttrs) {
        var attrs = { type: 'checkbox', value: value, class: 'mt-1 h-4 w-4 accent-[#7aa2f7] shrink-0' };
        if (name) attrs.name = name;
        Object.keys(extraAttrs || {}).forEach(function (k) { attrs[k] = extraAttrs[k]; });
        var input = el('input', attrs);
        var wrap = el('label', { class: 'flex items-start gap-3 cursor-pointer' }, [input, label]);
        return { wrap: wrap, input: input };
    }

    function render(slot, cfg, actionUrl) {
        var provider = detectProvider(actionUrl);
        var interests = interestFields(provider, cfg.signupInterestIds);
        var privacyHref = siteUrl('privacy.html');
        var uid = 'ad-signup';

        var form = el('form', {
            method: 'post',
            action: actionUrl.href,
            'accept-charset': 'UTF-8',
            class: 'space-y-5 text-left',
            novalidate: 'novalidate'
        });

        if (provider.target === 'frame') {
            var frameName = uid + '-sink';
            var frame = el('iframe', { name: frameName, title: 'Sign-up response', 'aria-hidden': 'true', tabindex: '-1', style: 'display:none' });
            slot.appendChild(frame);
            form.setAttribute('target', frameName);
        } else {
            form.setAttribute('target', '_blank');
        }

        // Email
        var emailInput = el('input', {
            type: 'email', name: provider.emailField, id: uid + '-email', required: 'required',
            autocomplete: 'email', placeholder: 'you@example.com',
            class: 'w-full px-4 py-3 rounded-lg bg-tn-bg border border-tn-border text-tn-text placeholder-tn-dim focus:outline-none focus:border-tn-accent'
        });
        form.appendChild(el('div', null, [
            el('label', { for: uid + '-email', class: 'block text-sm text-tn-dim mb-2', text: 'Email address' }),
            emailInput
        ]));

        // Interests
        var interestBoxes = [];
        if (interests) {
            var news = checkbox(interests.name, interests.newsletter,
                el('span', null, [el('span', { class: 'text-tn-text font-medium', text: 'Newsletter' }),
                    el('span', { class: 'block text-sm text-tn-dim', text: 'Release notes and tips, a few times a year.' })]));
            var mac = checkbox(interests.name, interests.macApp,
                el('span', null, [el('span', { class: 'text-tn-text font-medium', text: 'Mac app waitlist' }),
                    el('span', { class: 'block text-sm text-tn-dim', text: 'One email when the Mac app is ready.' })]));
            interestBoxes = [news.input, mac.input];
            form.appendChild(el('fieldset', { class: 'space-y-3' }, [
                el('legend', { class: 'text-sm text-tn-dim mb-2', text: 'What would you like?' }),
                news.wrap, mac.wrap
            ]));
        } else {
            form.appendChild(el('p', { class: 'text-sm text-tn-dim',
                text: 'You will get Agent Deck news: release notes, tips, and a note when the Mac app is ready.' }));
        }

        // Provider-specific hidden fields
        Object.keys(provider.hidden || {}).forEach(function (k) {
            form.appendChild(el('input', { type: 'hidden', name: k, value: provider.hidden[k] }));
        });

        // Consent (required, never pre-ticked)
        var privacyLink = el('a', { href: privacyHref, class: 'text-tn-accent hover:underline', text: 'Privacy policy' });
        var consent = checkbox(null, 'yes', el('span', { class: 'text-sm text-tn-dim' }, [
            'I agree to receive emails from Agent Deck about what I picked. I will first get an email to confirm my address (double opt-in), and I can unsubscribe any time with the link in every email. My address is stored by ' + provider.name + ', who sends the emails. See the ',
            privacyLink, '.'
        ]), { required: 'required', id: uid + '-consent' });
        form.appendChild(consent.wrap);

        var errorBox = el('p', { class: 'text-sm text-tn-red hidden', role: 'alert' });
        var doneBox = el('p', { class: 'text-sm text-tn-green hidden', role: 'status' });
        var button = el('button', {
            type: 'submit',
            class: 'w-full sm:w-auto px-6 py-3 rounded-lg bg-tn-accent text-tn-bg font-semibold hover:opacity-90 transition-opacity',
            text: 'Sign up'
        });
        form.appendChild(el('div', { class: 'flex flex-col sm:flex-row sm:items-center gap-4' }, [button, errorBox]));
        form.appendChild(doneBox);

        form.addEventListener('submit', function (ev) {
            var problem = '';
            if (!emailInput.value.trim() || !emailInput.checkValidity()) problem = 'Please enter a valid email address.';
            else if (interestBoxes.length && !interestBoxes.some(function (b) { return b.checked; })) problem = 'Please pick at least one: Newsletter or Mac app waitlist.';
            else if (!consent.input.checked) problem = 'Please tick the consent box so we may email you.';
            if (problem) {
                ev.preventDefault();
                errorBox.textContent = problem;
                errorBox.classList.remove('hidden');
                return;
            }
            errorBox.classList.add('hidden');
            doneBox.textContent = provider.target === 'frame'
                ? 'Almost done: check your inbox and click the link in the confirmation email.'
                : 'Almost done: finish in the new tab if asked, then click the link in the confirmation email.';
            doneBox.classList.remove('hidden');
        });

        var section = el('section', { id: 'signup', class: 'max-w-5xl mx-auto px-6 py-16', 'aria-labelledby': uid + '-title' }, [
            el('div', { class: 'max-w-2xl mx-auto bg-tn-surface border border-tn-border rounded-xl p-6 md:p-8' }, [
                el('div', { class: 'mono text-xs text-tn-dim mb-3', text: '[\u25cf\u2502\u25d0\u2502\u25cb] stay in the loop' }),
                el('h2', { id: uid + '-title', class: 'text-2xl md:text-3xl font-bold mb-2', text: 'The Mac app is coming. Get notified.' }),
                el('p', { class: 'text-tn-dim mb-6', text: 'Join the waitlist for the native Mac app, or get the occasional Agent Deck newsletter. No spam.' }),
                form
            ])
        ]);
        slot.appendChild(section);
    }

    function privacyIsLive() {
        var url = siteUrl('privacy.html');
        function ok(r) { return r && r.status === 200; }
        return fetch(url, { method: 'HEAD', cache: 'no-store', credentials: 'omit' })
            .then(function (r) {
                if (ok(r)) return true;
                if (r.status === 405 || r.status === 501) {
                    return fetch(url, { method: 'GET', cache: 'no-store', credentials: 'omit' }).then(ok);
                }
                return false;
            })
            .catch(function () { return false; });
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

        privacyIsLive().then(function (live) {
            if (!live) return;                  // no privacy page: render nothing
            render(slot, cfg, actionUrl);
        });
    }

    function start() { loadConfig(run); }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', start);
    } else {
        start();
    }
})();
