# Attribution and modification notice

This is an unofficial source extension for Wei-Shaw/sub2api.

- Upstream: https://github.com/Wei-Shaw/sub2api
- Baseline: v0.2.6, commit `49a39b6dc1abed30fd227611e8af1108bc427610`.
- License: GNU Lesser General Public License v3; see LICENSE and the incorporated GNU GPL v3 text in COPYING.GPL3. Existing upstream copyright notices remain applicable.
- Modifications dated 2026-09-18: per-account STATE controls, manual Pro/Team selection, global harvest pool integration, fixed-business-proxy verification, retention and renewal handling, response watchdog, admin UI, tests and documentation.
- The exact modified and added paths, baseline file hashes and overlay hashes are recorded in the community UPSTREAM.json (preserved here as sub2api-state-kit-UPSTREAM.json). A null baseline hash denotes an added file.

Design reference: https://github.com/gylive/ccodex-sleep-state at commit `26b22196bf68b372d0daad9381f686a3321068d4`, specifically ticket lifecycle and status presentation ideas. No source code or dependencies from that project are included in this extension. This implementation uses Sub2API account persistence and fixed-business-proxy verification.

Thanks to both upstream projects and community members for discussion and feedback. No affiliation or endorsement is implied.

XY2API adaptation: community commit ecf3b9acb6bd40ba9e920b1b21e3db446ecaaf27,
https://github.com/liulixin-lex/sub2api-state-kit . Adapted incrementally against
XY2API 6c12e3fcc421094b265ec5a50f0a2921105e4f03. Added independent model state,
PostgreSQL transactions, bounded complete-response validation and IQ integration.
No community sponsorship or advertising UI was imported.
