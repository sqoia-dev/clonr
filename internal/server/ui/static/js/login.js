// login.js — handles the /login page form submission (ADR-0007).
//
// Sends username+password to POST /api/v1/auth/login.
// On force_password_change=true, redirects to /set-password.

(function () {
    const form      = document.getElementById('login-form');
    const usernameEl = document.getElementById('username');
    const passwordEl = document.getElementById('password');
    const btn       = document.getElementById('login-btn');
    const errEl     = document.getElementById('login-error');

    // B2-2: Show default-credentials hint on fresh installs only.
    // Non-fatal — if the request fails we just don't show the hint.
    (async function checkBootstrapStatus() {
        try {
            const resp = await fetch('/api/v1/auth/bootstrap-status');
            if (!resp.ok) return;
            const data = await resp.json();
            const hint = document.getElementById('first-login-hint');
            if (hint && data.bootstrap_complete === false) {
                hint.style.display = '';
            }
        } catch (_) {}
    }());

    function showError(msg) {
        errEl.textContent = msg;
        errEl.classList.add('visible');
        usernameEl.focus();
    }

    function clearError() {
        errEl.classList.remove('visible');
        errEl.textContent = '';
    }

    form.addEventListener('submit', async function (e) {
        e.preventDefault();
        clearError();

        const username = (usernameEl.value || '').trim();
        const password = passwordEl.value || '';

        if (!username) {
            showError('Please enter your username.');
            return;
        }
        if (!password) {
            showError('Please enter your password.');
            return;
        }

        btn.disabled = true;
        btn.textContent = 'Signing in\u2026';

        try {
            const resp = await fetch('/api/v1/auth/login', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ username, password }),
                credentials: 'same-origin',
            });

            if (resp.ok) {
                let body = {};
                try { body = await resp.json(); } catch (_) {}

                // Clear any legacy localStorage key from the old modal flow.
                try { localStorage.removeItem('clustr_admin_key'); } catch (_) {}

                if (body.force_password_change) {
                    window.location.href = '/set-password';
                } else {
                    // Honour the ?next= param set by api.js when session expired.
                    // next is a URL-encoded hash like "%23%2Fnodes%2Fabc" so we
                    // redirect to "/" then append it directly as the hash.
                    const params = new URLSearchParams(window.location.search);
                    const next = params.get('next');
                    if (next) {
                        const decoded = decodeURIComponent(next);
                        // decoded should be a hash fragment starting with '#'
                        window.location.href = '/' + (decoded.startsWith('#') ? decoded : '#' + decoded);
                    } else {
                        window.location.href = '/';
                    }
                }
                return;
            }

            let msg = 'Invalid username or password.';
            try {
                const body = await resp.json();
                if (body && body.error) msg = body.error;
            } catch (_) {}
            showError(msg);
        } catch (err) {
            showError('Network error \u2014 could not reach the server.');
        } finally {
            btn.disabled = false;
            btn.textContent = 'Sign in';
        }
    });
}());
