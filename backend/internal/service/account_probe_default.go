package service

// InitializeNewAccountProbeDefault is shared by admin creation and repository
// insertion so imports, sync and duplication receive the same API-key default.
func InitializeNewAccountProbeDefault(account *Account) {
	if account.Type != AccountTypeAPIKey {
		return
	}
	if _, set := account.Extra[UpstreamBillingProbeEnabledExtraKey]; set {
		return
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[UpstreamBillingProbeEnabledExtraKey] = true
}
