// app-utils.test.mjs — Unit tests for pure JS utility functions from app.js.
// Run with: node --test test/js/app-utils.test.mjs
// Requires Node.js >= 20 (uses built-in node:test + node:assert).

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';

// ─── Inline the functions under test ────────────────────────────────────────
// These are copied verbatim from app.js to avoid a browser-only dependency.
// If the app.js implementations change, update these copies accordingly.

function fmtBytes(bytes) {
    if (!bytes || bytes === 0) return '—';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0, n = bytes;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return `${n.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function fmtRelative(ts, _nowOverride) {
    if (!ts) return '—';
    // Accept an optional nowMs override so tests can be deterministic.
    const nowMs = _nowOverride !== undefined ? _nowOverride : Date.now();
    const diff = nowMs - new Date(ts).getTime();
    const s = Math.floor(diff / 1000);
    if (s < 60)  return `${s}s ago`;
    const m = Math.floor(s / 60);
    if (m < 60)  return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24)  return `${h}h ago`;
    // Beyond 24h, fmtRelative falls through to fmtDateShort — we just check
    // that the return is a non-empty string (locale-dependent output).
    return new Date(ts).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
}

function _isoDetectDistro(url) {
    const lower = url.toLowerCase();
    const base  = lower.split('?')[0].split('/').pop();
    const verMatch = base.match(/[\-_](\d+\.\d+(?:\.\d+)?)/);
    const version  = verMatch ? verMatch[1] : '';
    if (lower.includes('rockylinux.org') || base.startsWith('rocky-')) {
        return { distro: 'rocky', os: 'Rocky Linux ' + (version || ''), version };
    }
    if (lower.includes('almalinux.org') || base.startsWith('almalinux-')) {
        return { distro: 'almalinux', os: 'AlmaLinux ' + (version || ''), version };
    }
    if (lower.includes('centos.org') || base.startsWith('centos-')) {
        return { distro: 'centos', os: 'CentOS ' + (version || ''), version };
    }
    if (lower.includes('ubuntu.com') || lower.includes('releases.ubuntu.com') || lower.includes('cdimage.ubuntu.com') || base.startsWith('ubuntu-')) {
        return { distro: 'ubuntu', os: 'Ubuntu ' + (version || ''), version };
    }
    if (lower.includes('debian.org') || base.startsWith('debian-')) {
        return { distro: 'debian', os: 'Debian ' + (version || ''), version };
    }
    if (lower.includes('opensuse.org') || lower.includes('suse.com') || base.startsWith('opensuse-') || base.startsWith('sle-')) {
        return { distro: 'suse', os: 'SUSE / openSUSE', version };
    }
    if (lower.includes('alpinelinux.org') || base.startsWith('alpine-')) {
        return { distro: 'alpine', os: 'Alpine Linux', version };
    }
    return { distro: '', os: '', version };
}

function _phaseLabel(phase) {
    const labels = {
        downloading_iso:   'Downloading ISO',
        generating_config: 'Generating config',
        creating_disk:     'Creating disk',
        launching_vm:      'Launching VM',
        installing:        'Installing OS',
        extracting:        'Extracting rootfs',
        scrubbing:         'Scrubbing identity',
        finalizing:        'Finalizing',
        complete:          'Complete',
        failed:            'Failed',
        canceled:          'Canceled',
    };
    return labels[phase] || phase;
}

function _phasePercent(phase) {
    const pcts = {
        downloading_iso:   10, generating_config: 20, creating_disk: 25,
        launching_vm: 30, installing: 60, extracting: 80,
        scrubbing: 90, finalizing: 95, complete: 100, failed: 100, canceled: 100,
    };
    return pcts[phase] || 5;
}

// ─── Tests ──────────────────────────────────────────────────────────────────

describe('fmtBytes', () => {
    test('returns dash for zero', () => {
        assert.equal(fmtBytes(0), '—');
    });

    test('returns dash for null/undefined', () => {
        assert.equal(fmtBytes(null), '—');
        assert.equal(fmtBytes(undefined), '—');
    });

    test('formats bytes under 1 KB', () => {
        assert.equal(fmtBytes(512), '512 B');
        assert.equal(fmtBytes(1023), '1023 B');
    });

    test('formats 1024 bytes as 1.0 KB', () => {
        assert.equal(fmtBytes(1024), '1.0 KB');
    });

    test('formats megabytes correctly (binary, not decimal)', () => {
        // 1 MB = 1024 * 1024 = 1,048,576 bytes
        assert.equal(fmtBytes(1024 * 1024), '1.0 MB');
        // 10 MB
        assert.equal(fmtBytes(10 * 1024 * 1024), '10.0 MB');
    });

    test('formats gigabytes correctly', () => {
        // 1 GB = 1024^3 bytes
        assert.equal(fmtBytes(1024 * 1024 * 1024), '1.0 GB');
    });

    test('formats terabytes correctly', () => {
        assert.equal(fmtBytes(1024 ** 4), '1.0 TB');
    });

    test('does NOT use decimal SI (1e9 would be wrong)', () => {
        // 1,000,000,000 bytes should NOT be 1.0 GB in binary mode.
        // It should be ~0.9 GB (931.3 MB to be precise).
        const result = fmtBytes(1_000_000_000);
        assert.ok(!result.endsWith('GB') || result.startsWith('0.'), `expected sub-1 GB, got ${result}`);
    });
});

describe('fmtRelative', () => {
    test('returns dash for null/undefined', () => {
        assert.equal(fmtRelative(null), '—');
        assert.equal(fmtRelative(undefined), '—');
    });

    test('formats seconds ago', () => {
        const now = Date.now();
        const ts = new Date(now - 30_000).toISOString();
        assert.equal(fmtRelative(ts, now), '30s ago');
    });

    test('formats minutes ago', () => {
        const now = Date.now();
        const ts = new Date(now - 5 * 60_000).toISOString();
        assert.equal(fmtRelative(ts, now), '5m ago');
    });

    test('formats hours ago', () => {
        const now = Date.now();
        const ts = new Date(now - 3 * 3600_000).toISOString();
        assert.equal(fmtRelative(ts, now), '3h ago');
    });

    test('returns formatted date string for >24h', () => {
        const now = Date.now();
        const ts = new Date(now - 2 * 24 * 3600_000).toISOString();
        const result = fmtRelative(ts, now);
        // Should not return "Xh ago" or "Xs ago" — returns locale date string.
        assert.ok(!result.endsWith('ago'), `expected date string, got ${result}`);
        assert.ok(result !== '—', 'should return a date, not dash');
    });

    test('boundary: exactly 59 seconds is "seconds ago"', () => {
        const now = Date.now();
        const ts = new Date(now - 59_000).toISOString();
        assert.ok(fmtRelative(ts, now).endsWith('s ago'));
    });

    test('boundary: exactly 60 seconds is "1m ago"', () => {
        const now = Date.now();
        const ts = new Date(now - 60_000).toISOString();
        assert.equal(fmtRelative(ts, now), '1m ago');
    });
});

describe('_isoDetectDistro', () => {
    test('detects Rocky Linux from domain', () => {
        const r = _isoDetectDistro('https://dl.rockylinux.org/pub/rocky/10/isos/x86_64/Rocky-10.0-x86_64-dvd.iso');
        assert.equal(r.distro, 'rocky');
        assert.ok(r.os.startsWith('Rocky Linux'));
    });

    test('detects Rocky Linux from filename prefix', () => {
        const r = _isoDetectDistro('https://mirror.example.com/rocky-10.1-x86_64-dvd.iso');
        assert.equal(r.distro, 'rocky');
        assert.equal(r.version, '10.1');
    });

    test('detects AlmaLinux from domain', () => {
        const r = _isoDetectDistro('https://repo.almalinux.org/almalinux/9.4/isos/x86_64/AlmaLinux-9.4-x86_64-dvd.iso');
        assert.equal(r.distro, 'almalinux');
    });

    test('detects Ubuntu from domain', () => {
        const r = _isoDetectDistro('https://releases.ubuntu.com/22.04/ubuntu-22.04.3-live-server-amd64.iso');
        assert.equal(r.distro, 'ubuntu');
        // Regex captures the first X.Y.Z token from the filename (22.04.3 in this case).
        assert.equal(r.version, '22.04.3');
    });

    test('detects Debian from domain', () => {
        const r = _isoDetectDistro('https://cdimage.debian.org/debian-cd/12.5.0/amd64/iso-cd/debian-12.5.0-amd64-netinst.iso');
        assert.equal(r.distro, 'debian');
        assert.equal(r.version, '12.5.0');
    });

    test('detects Alpine Linux from domain', () => {
        const r = _isoDetectDistro('https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-standard-3.19.1-x86_64.iso');
        assert.equal(r.distro, 'alpine');
    });

    test('returns empty strings for unknown URL', () => {
        const r = _isoDetectDistro('https://example.com/my-custom-os.iso');
        assert.equal(r.distro, '');
        assert.equal(r.os, '');
    });

    test('extracts version from complex filename', () => {
        const r = _isoDetectDistro('https://dl.rockylinux.org/pub/rocky-10.2.1-x86_64-dvd.iso');
        assert.equal(r.version, '10.2.1');
    });

    test('handles URL query strings gracefully', () => {
        const r = _isoDetectDistro('https://example.com/rocky-10.0-x86_64.iso?token=abc123');
        assert.equal(r.distro, 'rocky');
    });
});

describe('_phasePercent', () => {
    test('downloading_iso is 10%', () => {
        assert.equal(_phasePercent('downloading_iso'), 10);
    });

    test('installing is 60%', () => {
        assert.equal(_phasePercent('installing'), 60);
    });

    test('complete is 100%', () => {
        assert.equal(_phasePercent('complete'), 100);
    });

    test('failed is 100%', () => {
        assert.equal(_phasePercent('failed'), 100);
    });

    test('unknown phase defaults to 5%', () => {
        assert.equal(_phasePercent('some_unknown_phase'), 5);
    });

    test('all phases produce values between 5 and 100', () => {
        const knownPhases = [
            'downloading_iso', 'generating_config', 'creating_disk',
            'launching_vm', 'installing', 'extracting', 'scrubbing',
            'finalizing', 'complete', 'failed', 'canceled',
        ];
        for (const phase of knownPhases) {
            const pct = _phasePercent(phase);
            assert.ok(pct >= 5 && pct <= 100, `phase ${phase} produced out-of-range ${pct}`);
        }
    });

    test('phases are monotonically non-decreasing through the happy path', () => {
        const happyPath = [
            'downloading_iso', 'generating_config', 'creating_disk',
            'launching_vm', 'installing', 'extracting', 'scrubbing',
            'finalizing', 'complete',
        ];
        let prev = 0;
        for (const phase of happyPath) {
            const pct = _phasePercent(phase);
            assert.ok(pct >= prev, `phase ${phase} (${pct}%) should be >= previous (${prev}%)`);
            prev = pct;
        }
    });
});

describe('_phaseLabel', () => {
    test('returns human-readable labels for known phases', () => {
        assert.equal(_phaseLabel('downloading_iso'), 'Downloading ISO');
        assert.equal(_phaseLabel('installing'), 'Installing OS');
        assert.equal(_phaseLabel('complete'), 'Complete');
        assert.equal(_phaseLabel('failed'), 'Failed');
        assert.equal(_phaseLabel('canceled'), 'Canceled');
    });

    test('returns the raw phase string for unknown phases (graceful fallback)', () => {
        assert.equal(_phaseLabel('some_future_phase'), 'some_future_phase');
    });

    test('returns non-empty label for every phase that has a percent', () => {
        const knownPhases = [
            'downloading_iso', 'generating_config', 'creating_disk',
            'launching_vm', 'installing', 'extracting', 'scrubbing',
            'finalizing', 'complete', 'failed', 'canceled',
        ];
        for (const phase of knownPhases) {
            const label = _phaseLabel(phase);
            assert.ok(label && label.length > 0, `phase ${phase} should have a non-empty label`);
        }
    });
});

// ─── Alpine component helpers (Sprint B.5 — dhcpLeasesComponent) ─────────────
// These functions are inlined from the dhcpLeasesComponent() factory in app.js.
// If the factory implementation changes, update these copies accordingly.

function dhcpLeasesStateBadgeClass(state) {
    const map = {
        'deployed_verified':     'badge badge-deployed',
        'deploy_verify_timeout': 'badge badge-error',
        'deployed_preboot':      'badge badge-warning',
        'failed':                'badge badge-error',
        'reimage_pending':       'badge badge-info',
        'configured':            'badge badge-info',
        'registered':            'badge badge-warning',
    };
    return map[state] || 'badge badge-neutral';
}

function dhcpLeasesStateBadgeLabel(state) {
    const map = {
        'deployed_verified':     'Verified',
        'deploy_verify_timeout': 'Verify Timeout',
        'deployed_preboot':      'Unverified',
        'failed':                'Failed',
        'reimage_pending':       'Reimaging',
        'configured':            'Configured',
        'registered':            'Registered',
    };
    return map[state] || (state || 'Unknown');
}

describe('dhcpLeasesComponent — stateBadgeClass', () => {
    test('deployed_verified → badge-deployed', () => {
        assert.equal(dhcpLeasesStateBadgeClass('deployed_verified'), 'badge badge-deployed');
    });

    test('failed → badge-error', () => {
        assert.equal(dhcpLeasesStateBadgeClass('failed'), 'badge badge-error');
    });

    test('deploy_verify_timeout → badge-error', () => {
        assert.equal(dhcpLeasesStateBadgeClass('deploy_verify_timeout'), 'badge badge-error');
    });

    test('deployed_preboot → badge-warning', () => {
        assert.equal(dhcpLeasesStateBadgeClass('deployed_preboot'), 'badge badge-warning');
    });

    test('registered → badge-warning', () => {
        assert.equal(dhcpLeasesStateBadgeClass('registered'), 'badge badge-warning');
    });

    test('reimage_pending → badge-info', () => {
        assert.equal(dhcpLeasesStateBadgeClass('reimage_pending'), 'badge badge-info');
    });

    test('configured → badge-info', () => {
        assert.equal(dhcpLeasesStateBadgeClass('configured'), 'badge badge-info');
    });

    test('unknown state → badge-neutral (safe fallback)', () => {
        assert.equal(dhcpLeasesStateBadgeClass('some_unknown_state'), 'badge badge-neutral');
        assert.equal(dhcpLeasesStateBadgeClass(''), 'badge badge-neutral');
        assert.equal(dhcpLeasesStateBadgeClass(undefined), 'badge badge-neutral');
        assert.equal(dhcpLeasesStateBadgeClass(null), 'badge badge-neutral');
    });

    test('all mapped states produce a non-empty class string', () => {
        const states = [
            'deployed_verified', 'deploy_verify_timeout', 'deployed_preboot',
            'failed', 'reimage_pending', 'configured', 'registered',
        ];
        for (const s of states) {
            const cls = dhcpLeasesStateBadgeClass(s);
            assert.ok(cls && cls.startsWith('badge'), `state ${s} should produce a badge class, got ${cls}`);
        }
    });
});

describe('dhcpLeasesComponent — stateBadgeLabel', () => {
    test('deployed_verified → Verified', () => {
        assert.equal(dhcpLeasesStateBadgeLabel('deployed_verified'), 'Verified');
    });

    test('failed → Failed', () => {
        assert.equal(dhcpLeasesStateBadgeLabel('failed'), 'Failed');
    });

    test('deploy_verify_timeout → Verify Timeout', () => {
        assert.equal(dhcpLeasesStateBadgeLabel('deploy_verify_timeout'), 'Verify Timeout');
    });

    test('unknown state → returns the raw state string', () => {
        assert.equal(dhcpLeasesStateBadgeLabel('some_future_state'), 'some_future_state');
    });

    test('null/empty state → Unknown', () => {
        assert.equal(dhcpLeasesStateBadgeLabel(''), 'Unknown');
        assert.equal(dhcpLeasesStateBadgeLabel(null), 'Unknown');
        assert.equal(dhcpLeasesStateBadgeLabel(undefined), 'Unknown');
    });

    test('label + class are in sync — every mapped state has both a class and a label', () => {
        const states = [
            'deployed_verified', 'deploy_verify_timeout', 'deployed_preboot',
            'failed', 'reimage_pending', 'configured', 'registered',
        ];
        for (const s of states) {
            const cls   = dhcpLeasesStateBadgeClass(s);
            const label = dhcpLeasesStateBadgeLabel(s);
            assert.ok(cls !== 'badge badge-neutral', `${s} should have a specific class`);
            assert.ok(label !== s, `${s} should have a human-readable label, not the raw state`);
        }
    });
});
