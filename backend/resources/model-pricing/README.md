# Model Pricing Data

This directory contains a local copy of the mirrored model pricing data as a fallback mechanism.

## Source
The source data is maintained by LiteLLM. XY2API keeps the original runtime
download path through the Wei-Shaw `model-price-repo` mirror and verifies it
with the matching SHA-256 file:
- Default mirror: https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json
- Mirror checksum: https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.sha256
- LiteLLM upstream: https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json

## Purpose
This local copy serves as a fallback when the remote file cannot be downloaded due to:
- Network restrictions
- Firewall rules
- DNS resolution issues
- GitHub being blocked in certain regions
- Docker container network limitations

## Update Process
The pricingService will:
1. First attempt to download the latest version from GitHub
2. If download fails, use this local copy as fallback
3. Log a warning when using the fallback file

## Manual Update
To manually update this file with the latest pricing data (if automation is unavailable):
```bash
curl -s https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json -o model_prices_and_context_window.json
```

## File Format
The file contains JSON data with model pricing information including:
- Model names and identifiers
- Input/output token costs
- Context window sizes
- Model capabilities

Last updated: 2025-08-10
