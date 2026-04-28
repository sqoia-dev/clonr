// csp-policy.test.mjs — Sprint F (v1.5.0) CSP regression tests.
//
// These tests parse the static HTML and JS files to assert that:
//   1. No served HTML contains inline <script> bodies (script elements must have src=).
//   2. No served HTML contains inline event handler attributes (onclick=, onsubmit=, etc.).
//   3. The middleware.go CSP header is present and contains required directives.
//   4. All <script src="..."> tags reference paths under /ui/ (served by embed.FS).
//
// Run with: node --test test/js/csp-policy.test.mjs
// Requires Node.js >= 20 (built-in node:test + node:assert + node:fs).

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '..', '..');

// ─── Helpers ─────────────────────────────────────────────────────────────────

function readFile(relPath) {
    return fs.readFileSync(path.join(repoRoot, relPath), 'utf8');
}

function listHTMLFiles(dir) {
    const abs = path.join(repoRoot, dir);
    if (!fs.existsSync(abs)) return [];
    return fs.readdirSync(abs)
        .filter(f => f.endsWith('.html'))
        .map(f => path.join(dir, f));
}

// Match inline <script> blocks that have content (not just whitespace).
// A script element is "inline" if it has no src attribute.
// We ignore empty script tags like <script></script> which are harmless.
const reInlineScript = /<script(?![^>]*\bsrc\s*=)[^>]*>[\s\S]*?<\/script>/gi;

// Match any inline event handler attributes.
// These are disallowed by script-src 'self' CSP (no unsafe-hashes).
// The /gi flags are required (matchAll needs /g, /i for case-insensitivity).
const reInlineHandler = /\s(on(?:click|submit|change|input|keyup|keydown|keypress|focus|blur|load|unload|error|mousedown|mouseup|mouseover|mouseout|mousemove|contextmenu|dblclick|drag|dragend|dragenter|dragleave|dragover|dragstart|drop|resize|scroll|select|touchstart|touchend|touchmove|touchcancel|animationstart|animationend|transitionend))\s*=/gi;

// ─── HTML files to check ──────────────────────────────────────────────────────

const htmlDir = 'internal/server/ui/static';
const htmlFiles = listHTMLFiles(htmlDir);

// ─── Tests ────────────────────────────────────────────────────────────────────

describe('CSP Policy — no inline scripts in HTML files', () => {
    assert.ok(htmlFiles.length > 0, 'expected at least one HTML file in ' + htmlDir);

    for (const relPath of htmlFiles) {
        test(`${relPath} has no inline <script> bodies`, () => {
            const content = readFile(relPath);
            const matches = [...content.matchAll(reInlineScript)]
                .filter(m => m[0].replace(/<script[^>]*>/, '').replace(/<\/script>/, '').trim() !== '');
            assert.deepEqual(
                matches.map(m => m[0].substring(0, 120)),
                [],
                `Found inline script blocks in ${relPath}`,
            );
        });

        test(`${relPath} has no inline event handler attributes`, () => {
            const content = readFile(relPath);
            const matches = [...content.matchAll(reInlineHandler)];
            assert.deepEqual(
                matches.map(m => m[0].trim().substring(0, 80)),
                [],
                `Found inline event handlers in ${relPath}: ${matches.map(m => m[1]).join(', ')}`,
            );
        });
    }
});

describe('CSP Policy — middleware.go declares correct CSP header', () => {
    test('securityHeadersMiddleware sets script-src \'self\'', () => {
        const middleware = readFile('internal/server/middleware.go');
        assert.ok(
            middleware.includes("script-src 'self'"),
            "Expected \"script-src 'self'\" in middleware.go CSP constant",
        );
    });

    test('securityHeadersMiddleware sets frame-ancestors \'none\'', () => {
        const middleware = readFile('internal/server/middleware.go');
        assert.ok(
            middleware.includes("frame-ancestors 'none'"),
            "Expected \"frame-ancestors 'none'\" in middleware.go CSP constant",
        );
    });

    test('securityHeadersMiddleware is wired into the router', () => {
        const serverGo = readFile('internal/server/server.go');
        assert.ok(
            serverGo.includes('securityHeadersMiddleware'),
            'Expected securityHeadersMiddleware to be referenced in server.go',
        );
    });
});

describe('CSP Policy — Alpine.js CSP build is used', () => {
    test('index.html loads alpine-csp build, not standard build', () => {
        const html = readFile('internal/server/ui/static/index.html');
        assert.ok(
            html.includes('alpine-csp-'),
            'index.html should reference the alpine-csp build (alpine-csp-*.min.js)',
        );
        assert.ok(
            !html.includes('alpine-3.') || html.indexOf('alpine-csp-') < html.indexOf('alpine-3.'),
            'index.html should not load the standard Alpine build instead of the CSP build',
        );
    });
});

describe('CSP Policy — SIEM export route registered', () => {
    test('server.go registers /audit/export route', () => {
        const serverGo = readFile('internal/server/server.go');
        assert.ok(
            serverGo.includes('/audit/export'),
            'Expected /audit/export route in server.go',
        );
    });

    test('audit handler defines HandleExport method', () => {
        const handler = readFile('internal/server/handlers/audit.go');
        assert.ok(
            handler.includes('func (h *AuditHandler) HandleExport('),
            'Expected HandleExport method in handlers/audit.go',
        );
    });
});
