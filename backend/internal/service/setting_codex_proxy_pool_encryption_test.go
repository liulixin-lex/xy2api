package service

import (
	"context"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	apperrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// Fail immediately if a rejected operation reaches cryptography: checking only
// that Encrypt exists or that the process key is non-empty is insufficient.
type codexForbiddenCipher struct{}

func (codexForbiddenCipher) Encrypt(string) (string, error) { panic("unexpected encryption") }
func (codexForbiddenCipher) Decrypt(string) (string, error) { panic("unexpected decryption") }

func requireCodexPersistentKeyError(t *testing.T, err error) {
	t.Helper()
	require.Equal(t, 400, apperrors.Code(err))
	require.Equal(t, "CODEX_PROXY_ENCRYPTION_REQUIRED", apperrors.Reason(err))
	require.Contains(t, err.Error(), "TOTP_ENCRYPTION_KEY")
}

func TestCodexReliabilityProxyEncryptionMigrationRequiresPersistentKey(t *testing.T) {
	ctx := context.Background()
	for _, source := range []string{"database", "configuration", "nil_config"} {
		t.Run(source, func(t *testing.T) {
			svc, repo := reliabilityPoolService(t)
			legacy := repo.values[SettingKeyOpenAICodexTicketHarvestProxyURL]
			// Match config.Load's generated, non-empty key with configured=false.
			svc.cfg.Totp.EncryptionKey = strings.Repeat("ab", 32)
			svc.cfg.Totp.EncryptionKeyConfigured = false
			if source == "configuration" {
				delete(repo.values, SettingKeyOpenAICodexTicketHarvestProxyURL)
				svc.cfg.Gateway.OpenAICodexTicket.HarvestProxyURL = legacy
			} else if source == "nil_config" {
				svc.cfg = nil
			}
			svc.codexProxyEncryptor = codexForbiddenCipher{}
			_, err := svc.GetCodexHarvestProxyPool(ctx)
			requireCodexPersistentKeyError(t, err)
			require.NotContains(t, repo.values, SettingKeyCodexTicketProxyPool)
			if source != "configuration" {
				require.Equal(t, legacy, repo.values[SettingKeyOpenAICodexTicketHarvestProxyURL])
			}
		})
	}
}

func TestCodexReliabilityProxyEncryptionEmptyPoolNeedsNoKey(t *testing.T) {
	ctx := context.Background()
	repo := &reliabilitySettings{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	// Empty pool initialization must not require even an encryptor instance.
	pool, err := svc.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	require.Empty(t, pool.Entries)
	pool, err = svc.SaveCodexHarvestProxyPool(ctx, CodexHarvestProxyPoolUpdate{ExpectedRevision: pool.Revision, Entries: []CodexHarvestProxyInput{}})
	require.NoError(t, err)
	before := repo.values[SettingKeyCodexTicketProxyPool]
	proxy := "http://user:private@example.com:8080"
	_, err = svc.SaveCodexHarvestProxyPool(ctx, CodexHarvestProxyPoolUpdate{ExpectedRevision: pool.Revision, Entries: []CodexHarvestProxyInput{{Enabled: true, URL: &proxy}}})
	requireCodexPersistentKeyError(t, err)
	require.Equal(t, before, repo.values[SettingKeyCodexTicketProxyPool])

	// Removing all entries remains possible without decrypting old credentials.
	populated, populatedRepo := reliabilityPoolService(t)
	old, err := populated.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	populated.cfg.Totp.EncryptionKeyConfigured = false
	populated.codexProxyEncryptor = codexForbiddenCipher{}
	deleted, err := populated.SaveCodexHarvestProxyPool(ctx, CodexHarvestProxyPoolUpdate{ExpectedRevision: old.Revision, Entries: []CodexHarvestProxyInput{}})
	require.NoError(t, err)
	require.Empty(t, deleted.Entries)
	restarted := NewSettingService(populatedRepo, &config.Config{})
	authoritative, err := restarted.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	require.Empty(t, authoritative.Entries, "legacy proxy cannot resurrect after deletion")
}

func TestCodexReliabilityProxyEncryptionExistingPoolGuardsBeforeWrite(t *testing.T) {
	ctx := context.Background()
	svc, repo := reliabilityPoolService(t)
	pool, err := svc.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	before := repo.values[SettingKeyCodexTicketProxyPool]
	svc.cfg.Totp.EncryptionKeyConfigured = false
	svc.codexProxyEncryptor = codexForbiddenCipher{}
	_, err = svc.GetCodexHarvestProxyPool(ctx)
	requireCodexPersistentKeyError(t, err)
	proxy := "http://replacement:secret@second.example:8080"
	for _, address := range []*string{nil, &proxy} {
		_, err = svc.SaveCodexHarvestProxyPool(ctx, CodexHarvestProxyPoolUpdate{ExpectedRevision: pool.Revision, Entries: []CodexHarvestProxyInput{{ID: pool.Entries[0].ID, Name: "renamed", Enabled: true, URL: address}}})
		requireCodexPersistentKeyError(t, err)
		require.Equal(t, before, repo.values[SettingKeyCodexTicketProxyPool])
	}
	updates := map[string]string{SettingKeyOpenAICodexTicketHarvestProxyURL: proxy}
	_, err = svc.prepareLegacyCodexProxyUpdate(ctx, updates)
	requireCodexPersistentKeyError(t, err)
	require.Equal(t, map[string]string{SettingKeyOpenAICodexTicketHarvestProxyURL: proxy}, updates)
	require.Equal(t, before, repo.values[SettingKeyCodexTicketProxyPool])
}

func TestCodexReliabilityProxyEncryptionConfiguredRestart(t *testing.T) {
	ctx := context.Background()
	svc, repo := reliabilityPoolService(t)
	pool, err := svc.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	restarted := NewSettingService(repo, &config.Config{Totp: svc.cfg.Totp})
	restarted.codexProxyEncryptor = reliabilityCipher{}
	loaded, err := restarted.GetCodexHarvestProxyPool(ctx)
	require.NoError(t, err)
	require.Equal(t, pool, loaded)
	// Even with a stable config, a missing cipher must return an error, not panic.
	restarted.codexProxyEncryptor = nil
	_, err = restarted.GetCodexHarvestProxyPool(ctx)
	require.Equal(t, 503, apperrors.Code(err))
	require.Equal(t, "CODEX_PROXY_ENCRYPTION", apperrors.Reason(err))
}
