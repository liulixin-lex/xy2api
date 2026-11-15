package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
)

type pricingCacheSource struct {
	Hash   string `json:"hash"`
	Source string `json:"source"`
}

// Only a marker tied to these exact bytes can certify a cached base origin.
// Old caches without a marker remain unknown instead of claiming catalog data.
func (s *PricingService) cachedPricingBodySource(body []byte) string {
	raw, err := os.ReadFile(s.getPricingFilePath() + ".source")
	if err != nil {
		return ""
	}
	var marker pricingCacheSource
	if json.Unmarshal(raw, &marker) != nil {
		return ""
	}
	hash := sha256.Sum256(body)
	if marker.Hash != hex.EncodeToString(hash[:]) {
		return ""
	}
	if marker.Source != "catalog" && marker.Source != "fallback_file" {
		return ""
	}
	return marker.Source
}
