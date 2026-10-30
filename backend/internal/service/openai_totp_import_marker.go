package service

import "maps"

// This random provenance marker is not a credential. Only the typed server-side
// login importer may create it. Generic account APIs cannot copy, erase or
// replace it, including duplicate, extra-only and bulk writes.
const ManagedOpenAILoginMarkerExtraKey = "managed_openai_login_marker"

func managedOpenAICreateExtra(input *CreateAccountInput) map[string]any {
	extra := maps.Clone(input.Extra)
	delete(extra, ManagedOpenAILoginMarkerExtraKey)
	if input.ManagedOpenAILoginMarker != "" {
		if extra == nil {
			extra = map[string]any{}
		}
		extra[ManagedOpenAILoginMarkerExtraKey] = input.ManagedOpenAILoginMarker
	}
	return extra
}
func preserveManagedOpenAIImportMarker(extra, current map[string]any) map[string]any {
	extra = maps.Clone(extra)
	delete(extra, ManagedOpenAILoginMarkerExtraKey)
	if marker, ok := current[ManagedOpenAILoginMarkerExtraKey].(string); ok && marker != "" {
		if extra == nil {
			extra = map[string]any{}
		}
		extra[ManagedOpenAILoginMarkerExtraKey] = marker
	}
	return extra
}
