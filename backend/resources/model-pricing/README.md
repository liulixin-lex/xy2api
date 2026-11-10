# Model Pricing Data

This directory contains `model_prices_and_context_window.json`, the bundled pricing catalog. Docker images copy `backend/resources` to `/app/resources`, where the default `pricing.fallback_file` (`./resources/model-pricing/model_prices_and_context_window.json`, relative to the working directory) finds it. Binary release archives do not include this directory; there, point `pricing.fallback_file` at a copy of this file if you need the fallback.

The bundled file is not a full copy of the remote catalog. It is a curated subset that covers the models XY2API serves. Entries are copied verbatim from the remote catalog, and entries for newly supported models are added the same way.

## Source
Runtime pricing comes from a remote catalog in LiteLLM format. XY2API downloads it from the Wei-Shaw `model-price-repo` mirror by default:
- Default mirror (`pricing.remote_url`): https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json
- Mirror checksum (`pricing.hash_url`): https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.sha256
- LiteLLM upstream (format origin): https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json

The checksum file is used to detect a new catalog version. If the downloaded data does not match it, the mismatch is logged as a warning and the update still proceeds.

## How This File Is Used
The pricing service (`internal/service/pricing_service.go`) uses this file in three ways:
1. **Gap filler (always).** Every time the service builds the price table, from a download or from the cached copy in `{pricing.data_dir}/model_pricing.json`, it merges this file in. A model missing from the catalog is taken from this file. A model present in both keeps the catalog entry. Merged models are logged as `[Pricing] Merged N fallback-only models`.
2. **Startup fallback.** At startup the service loads the cached copy and checks the remote hash, or the file age when `pricing.hash_url` is empty. If there is no usable cached copy and the download fails, it logs `[Pricing] Initial load failed, using fallback`, copies this file to `{pricing.data_dir}/model_pricing.json`, and uses it as the catalog.
3. **Hot reload.** Every `pricing.hash_check_interval_minutes` the service checks the remote hash and compares the content of this file and of `pricing.override_file`. A changed file is reloaded without a restart.

`pricing.override_file` (optional) has the highest priority. Its entries are shallow-merged field by field over the catalog and this file, and a `null` field value removes that field. Override entries for models that exist in neither layer are added only if they carry price fields. Entries without any price fields, and the `sample_spec` entry, are ignored.

## Updating This File
Do not replace this file with a full download of the remote or LiteLLM catalog. That would discard the curated subset and any entries the remote catalog lacks.

1. Download the catalog that `pricing.remote_url` points to and check it against the published SHA-256:
   ```bash
   curl -fsSL -o /tmp/remote.json https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json
   curl -fsSL https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.sha256
   shasum -a 256 /tmp/remote.json
   ```
2. Refresh the bundled entries from the remote catalog and add new models, keeping entries the remote catalog does not have. From this directory, list any new model names after the two file arguments:
   ```bash
   python3 - /tmp/remote.json model_prices_and_context_window.json new-model-name <<'EOF'
   import json, sys
   remote_path, bundled_path, *new_models = sys.argv[1:]
   remote = json.load(open(remote_path, encoding="utf-8"))
   bundled = json.load(open(bundled_path, encoding="utf-8"))
   for name in new_models:
       if name not in remote:
           sys.exit(f"{name} is not in the remote catalog")
   for name in list(bundled) + new_models:
       if name in remote:
           bundled[name] = remote[name]  # copied verbatim; entries missing remotely are kept
   with open(bundled_path, "w", encoding="utf-8") as f:
       json.dump(bundled, f, indent=2, ensure_ascii=False)
       f.write("\n")
   EOF
   ```
   The script keeps the existing order and appends new models at the end. It also normalizes number formatting and indentation, so review the diff and keep only the intended changes.
3. Run the unit tests that read this file. They pin specific prices and check its data contracts, such as cache-tier fields having a base price and long-context ladders not being one-sided:
   ```bash
   cd backend
   go test -tags=unit ./internal/service/ ./internal/config/
   ```

## File Format
The file contains JSON data with model pricing information including:
- Model names and identifiers
- Input/output token costs
- Context window sizes
- Model capabilities
