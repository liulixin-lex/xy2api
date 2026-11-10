# Agent Skills

[Agent Skills](https://agentskills.io/) for AI coding assistants (Claude Code, Codex and other
clients that support the `SKILL.md` format) that operate an XY2API deployment.

| Skill | What it does |
|---|---|
| [`xy2api-admin`](xy2api-admin/SKILL.md) | Manage accounts, groups, proxies, redeem codes, error passthrough rules and TLS fingerprint profiles through the admin API, with a bundled Node.js CLI |

To use one, copy or link its directory into your assistant's skills directory and set
`XY2API_BASE_URL` plus `XY2API_ADMIN_API_KEY` (or `XY2API_JWT`). The admin API key grants full
administrator access; keep it out of shared shells and chat transcripts.
