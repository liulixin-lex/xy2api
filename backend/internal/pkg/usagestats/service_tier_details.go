package usagestats

import "encoding/json"

// PublicServiceTierDetails returns the customer-visible billing evidence.
func PublicServiceTierDetails(details json.RawMessage) json.RawMessage {
	if len(details) == 0 {
		return nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(details, &all); err != nil {
		return nil
	}
	visible := make(map[string]json.RawMessage)
	for _, key := range []string{
		"version", "client_presence", "client_tier", "outbound_tier", "observed_tier", "settled_tier",
		"billing_model", "price_source", "multiplier_source", "tier_multiplier", "free_tier",
		"standard_actual_cost", "outbound_actual_cost", "actual_cost", "cap_reason",
	} {
		if value, ok := all[key]; ok {
			visible[key] = value
		}
	}
	if len(visible) == 0 {
		return nil
	}
	result, _ := json.Marshal(visible)
	return result
}
