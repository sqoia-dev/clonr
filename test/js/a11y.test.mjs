// a11y.test.mjs — WCAG 2.1 AA accessibility audit for clustr HTML pages.
//
// Uses axe-core via jsdom to run rule checks against static HTML.
// Alpine.js + HTMX dynamic content is not evaluated (no live server);
// the audit covers the static DOM structure: landmarks, labels, language,
// headings, ARIA attributes, and colour contrast where detectable statically.
//
// Run with: node --test test/js/a11y.test.mjs
// Requires Node.js >= 20 and: npm install --prefix test/js axe-core jsdom
//
// CI runs: make a11y  (see Makefile)
//
// Waived items (Moderate, documented in docs/accessibility.md):
//   - skip-to-main links absent on portal pages (simple single-column layouts)
//   - Color contrast checks on dynamic Alpine state (x-show elements) are not
//     evaluated statically — the visible states are all on white/dark backgrounds
//     that meet 4.5:1 per CSS variable inspection.

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '..', '..');

// ─── Dynamic imports for npm packages ────────────────────────────────────────
// We install axe-core and jsdom as test-only devDependencies via:
//   npm install --prefix test/js axe-core jsdom
// They are never bundled into the Go binary.

const { JSDOM } = require(path.join(__dirname, 'node_modules', 'jsdom'));
const axe = require(path.join(__dirname, 'node_modules', 'axe-core'));

// ─── Pages to audit ──────────────────────────────────────────────────────────

const pages = [
    'internal/server/ui/static/index.html',
    'internal/server/ui/static/login.html',
    'internal/server/ui/static/portal.html',
    'internal/server/ui/static/portal_director.html',
    'internal/server/ui/static/portal_pi.html',
    'internal/server/ui/static/set-password.html',
];

// ─── axe-core WCAG 2.1 AA rules ──────────────────────────────────────────────

const AXE_OPTIONS = {
    runOnly: {
        type: 'tag',
        values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'],
    },
    // Rules that require a running browser / computed styles cannot be
    // evaluated statically via jsdom. We exclude only rules that jsdom
    // structurally cannot evaluate (not fixes we need to defer).
    rules: {
        // Color contrast requires computed CSS — jsdom doesn't compute cascade
        // from external stylesheets. The styles are verified manually; waived.
        'color-contrast': { enabled: false },
        // scrollable-region-focusable triggers on x-show hidden elements
        // that jsdom treats as visible because it doesn't run Alpine.
        'scrollable-region-focusable': { enabled: false },
    },
};

// ─── Helpers ─────────────────────────────────────────────────────────────────

/**
 * Load an HTML file and return an axe-core result via jsdom.
 * We neutralise x-show so jsdom doesn't evaluate Alpine attributes as broken.
 */
async function runAxe(relPath) {
    const absPath = path.join(repoRoot, relPath);
    let html = fs.readFileSync(absPath, 'utf8');

    // Strip external script tags so jsdom doesn't try to fetch them.
    // axe-core tests DOM structure only; script execution is not needed.
    html = html.replace(/<script\b[^>]*src="[^"]*"[^>]*><\/script>/gi, '');

    const dom = new JSDOM(html, {
        runScripts: 'outside-only',
        resources: 'usable',
        url: 'http://localhost/',
    });

    // Inject axe-core source into the DOM context
    const axeSource = fs.readFileSync(
        path.join(__dirname, 'node_modules', 'axe-core', 'axe.min.js'),
        'utf8',
    );
    dom.window.eval(axeSource);

    // Run axe
    const results = await dom.window.axe.run(dom.window.document, AXE_OPTIONS);
    return results;
}

/**
 * Format a violation for readable assertion output.
 */
function formatViolation(v) {
    const nodes = v.nodes.map(n => n.html.substring(0, 120)).join('\n    ');
    return `[${v.impact}] ${v.id}: ${v.description}\n  Nodes:\n    ${nodes}`;
}

// ─── Tests ────────────────────────────────────────────────────────────────────

describe('WCAG 2.1 AA — static HTML audit', () => {
    for (const page of pages) {
        test(`${page} passes axe-core WCAG 2.1 AA rules`, async () => {
            const results = await runAxe(page);

            // Filter to only critical and serious violations (hard AA fails).
            // Moderate findings are informational and documented in accessibility.md.
            const blocking = results.violations.filter(
                v => v.impact === 'critical' || v.impact === 'serious',
            );

            if (blocking.length > 0) {
                const report = blocking.map(formatViolation).join('\n\n');
                assert.fail(
                    `${blocking.length} critical/serious axe violation(s) in ${page}:\n\n${report}`,
                );
            }

            // Log moderate + minor findings as informational (do not fail).
            const informational = results.violations.filter(
                v => v.impact === 'moderate' || v.impact === 'minor',
            );
            if (informational.length > 0) {
                const names = informational.map(v => `${v.id}(${v.impact})`).join(', ');
                // Use process.stderr so node:test doesn't suppress it
                process.stderr.write(
                    `  [a11y info] ${page}: ${informational.length} moderate/minor finding(s) noted (not blocking): ${names}\n`,
                );
            }
        });
    }
});
