# Cache panel contract fixtures

The JSON schema, twenty-one synthetic examples, twenty-four rejected examples and validator are copied byte for byte from the shared F contract, revision v1.0.3 (only the source README changed after v1.0.2). SHA256SUMS retains the upstream checksums for those files. The original coordination README contains private workspace paths, so this public provenance note replaces it.

TestCacheNorthstarHandlerContract replays M2 requests through the HTTP handler and real query service with synthetic repository rows. The cost-unavailable case checks the typed DTO shape. The M3 experiments preview is reference data only and is excluded from M2 replay.

Set CACHE_PANEL_CONTRACT_OUTPUT to export actual handler envelopes. Run python3 -I validate_cache_panel_contract.py <output-directory> to check the schema, semantics and privacy of those responses. Run the same command on this fixture directory to verify all accepted and rejected reference examples.

R16 masks email-shaped account names as `<meta.platform>#<key>`; group labels use `group#<key>`. Actual HTTP regressions verify unchanged ordinary names and stable output across windows.

R17: priced_models counts priced (platform, model_key) pairs represented in the strict result. Prices and priced_p_share use business rows; all cost views use the same model count. Models with only below-minimum or unknown-threshold rows do not count in northstar priced_models.
