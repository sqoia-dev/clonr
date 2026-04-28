// set-password.js — Force-change password page script.
// Extracted from set-password.html inline <script> block in Sprint F (v1.5.0)
// to satisfy script-src 'self' CSP.  No logic changes.

(function () {
    const form    = document.getElementById('sp-form');
    const curEl   = document.getElementById('current-password');
    const newEl   = document.getElementById('new-password');
    const conEl   = document.getElementById('confirm-password');
    const btn     = document.getElementById('sp-btn');
    const errEl   = document.getElementById('sp-error');

    function showError(msg) {
        errEl.textContent = msg;
        errEl.classList.add('visible');
    }
    function clearError() {
        errEl.classList.remove('visible');
        errEl.textContent = '';
    }

    // If the force-change cookie is not set, redirect to main app.
    // (Handles direct navigation to /set-password after password already changed.)
    function readCookie(name) {
        return document.cookie.split(';').some(c => c.trim().startsWith(name + '='));
    }

    form.addEventListener('submit', async function (e) {
        e.preventDefault();
        clearError();

        const current = curEl.value;
        const newPw   = newEl.value;
        const confirm = conEl.value;

        if (newPw.length < 8) {
            showError('Password must be at least 8 characters.');
            return;
        }
        if (newPw !== confirm) {
            showError('Passwords do not match.');
            return;
        }

        btn.disabled = true;
        btn.textContent = 'Saving…';

        try {
            const resp = await fetch('/api/v1/auth/set-password', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ current_password: current, new_password: newPw }),
                credentials: 'same-origin',
            });

            if (resp.ok) {
                // B2-8: honour ?next= param if present (set by auth boot redirect).
                const params = new URLSearchParams(window.location.search);
                const next = params.get('next');
                if (next) {
                    const decoded = decodeURIComponent(next);
                    window.location.href = '/' + (decoded.startsWith('#') ? decoded : '#' + decoded);
                } else {
                    window.location.href = '/';
                }
                return;
            }

            let msg = 'Failed to set password.';
            try {
                const body = await resp.json();
                if (body && body.error) msg = body.error;
            } catch (_) {}
            showError(msg);
        } catch (err) {
            showError('Network error — could not reach the server.');
        } finally {
            btn.disabled = false;
            btn.textContent = 'Set password';
        }
    });
}());
