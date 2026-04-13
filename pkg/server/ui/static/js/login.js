// login.js — handles the /login page form submission.

(function () {
    const form   = document.getElementById('login-form');
    const input  = document.getElementById('api-key');
    const btn    = document.getElementById('login-btn');
    const errEl  = document.getElementById('login-error');

    function showError(msg) {
        errEl.textContent = msg;
        errEl.classList.add('visible');
        input.focus();
    }

    function clearError() {
        errEl.classList.remove('visible');
        errEl.textContent = '';
    }

    form.addEventListener('submit', async function (e) {
        e.preventDefault();
        clearError();

        const key = input.value.trim();
        if (!key) {
            showError('Please enter your admin API key.');
            return;
        }

        btn.disabled = true;
        btn.textContent = 'Signing in…';

        try {
            const resp = await fetch('/api/v1/auth/login', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ key }),
                credentials: 'same-origin',
            });

            if (resp.ok) {
                // Session cookie is now set. Redirect to main UI.
                // Clear any legacy localStorage key from the old modal flow.
                try { localStorage.removeItem('clonr_admin_key'); } catch (_) {}
                window.location.href = '/';
                return;
            }

            let msg = 'Invalid API key.';
            try {
                const body = await resp.json();
                if (body && body.error) msg = body.error;
            } catch (_) {}
            showError(msg);
        } catch (err) {
            showError('Network error — could not reach the server.');
        } finally {
            btn.disabled = false;
            btn.textContent = 'Sign in';
        }
    });
}());
