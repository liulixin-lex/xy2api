# Third-Party Notices

XY2API is licensed under the [GNU Lesser General Public License v3.0 or later](LICENSE).
The LGPL v3 incorporates the terms of the [GNU General Public License v3.0](https://www.gnu.org/licenses/gpl-3.0.txt).

This file lists the projects XY2API is derived from or that it adapts code or data from.
Dependencies installed through `backend/go.mod`, `frontend/package.json` and
`tools/openai-totp-runtime/package-lock.json` carry their own licenses, recorded in those
lock files and in the packages themselves.

## Sub2API

XY2API is a fork of [Sub2API](https://github.com/Wei-Shaw/sub2api).

- Copyright (c) 2026 Wesley Liddick. Licensed under LGPL-3.0.
- XY2API merged upstream releases up to Sub2API v0.2.15. `backend/cmd/server/SUB2API_COMPAT_VERSION`
  records that baseline; plugins use it to check compatibility.
- Upstream copyright notices in individual files remain in effect.
- XY2API modifications: Copyright (c) 2026 liulixin-lex, under the same license.

The Codex GPT-6.1 Sol support (`backend/internal/pkg/openai/codex_gpt61_sol.json` and the related
gateway code) was adapted from Sub2API v0.2.11.

## sub2api-state-kit

The Codex STATE feature started from the community extension
[sub2api-state-kit](https://github.com/liulixin-lex/sub2api-state-kit) at commit
`ecf3b9acb6bd40ba9e920b1b21e3db446ecaaf27` (LGPL-3.0, an extension of Sub2API v0.2.6), then was
extended in XY2API with per-model state, PostgreSQL transactions, bounded response validation and
IQ check integration. No sponsorship or advertising UI was imported.

Ideas, but no source code, were taken from:

| Project | License | What was used |
|---|---|---|
| [ccodex-sleep-state](https://github.com/gylive/ccodex-sleep-state) | GPL-3.0 | Ticket lifecycle, status presentation, rate-limit classification |
| [cpa-plugin-codex-turn-state](https://github.com/liulixin-lex/cpa-plugin-codex-turn-state) | MIT | Per-account and per-model expiry display |
| [codex-turn-state-cache](https://github.com/liulixin-lex/codex-turn-state-cache) | MIT | Response header handling |
| [Fernet specification](https://github.com/fernet/spec/blob/master/Spec.md) | — | Token envelope parsing |

## ranxi2001/sub2api

Parts of two features follow [ranxi2001/sub2api](https://github.com/ranxi2001/sub2api) at commit
`12a49ef4ef6a2f116389701ac8f1fe620f27bbf5` (LGPL-3.0), adapted to XY2API's permission model and
import flow:

- the support ticket implementation (the live support chat is XY2API's own);
- the managed OpenAI TOTP rotation state machine and its local encrypted recovery journal.

## OpenAI TOTP worker

- `tools/openai_totp_rotation.py`, `tools/openai_totp_journal.mjs` and their tests are adapted from
  the same-named files of the ranxi2001/sub2api commit above.
- `tools/openai-totp-runtime/source.json` pins [poxiao33/toSub2](https://github.com/poxiao33/toSub2)
  at commit `8548397e89bf80e508eda64a87e0d556d43abc84` (MIT, Copyright (c) 2026 poxiao33). Its
  source is not included here; the operator installs it and must keep its LICENSE.
- `tools/openai-totp-runtime/package*.json` derive from the `tools/reauth-runtime/` dependency lock
  of the same Sub2API commit.

## Model pricing data

`backend/resources/model-pricing/model_prices_and_context_window.json` is a fallback copy of the
[LiteLLM](https://github.com/BerriAI/litellm) model price list (MIT), fetched at runtime through
the [Wei-Shaw/model-price-repo](https://github.com/Wei-Shaw/model-price-repo) mirror.

---

No affiliation with or endorsement by any of these projects is implied.
