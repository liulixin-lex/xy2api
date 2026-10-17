//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRegistrationEmailSuffixWhitelist(t *testing.T) {
	got, err := NormalizeRegistrationEmailSuffixWhitelist([]string{"example.com", "@EXAMPLE.COM", " @foo.bar ", "*.EDU.CN"})
	require.NoError(t, err)
	require.Equal(t, []string{"@example.com", "@foo.bar", "*.edu.cn"}, got)
}

func TestNormalizeRegistrationEmailSuffixWhitelist_Invalid(t *testing.T) {
	for _, item := range []string{"@invalid_domain", "*.", "*", "*.@", "*.foo"} {
		t.Run(item, func(t *testing.T) {
			_, err := NormalizeRegistrationEmailSuffixWhitelist([]string{item})
			require.Error(t, err)
		})
	}
}

func TestParseRegistrationEmailSuffixWhitelist(t *testing.T) {
	got := ParseRegistrationEmailSuffixWhitelist(`["example.com","@foo.bar","*.EDU.CN","@invalid_domain","*.foo"]`)
	require.Equal(t, []string{"@example.com", "@foo.bar", "*.edu.cn"}, got)
}

func TestIsRegistrationEmailSuffixAllowed(t *testing.T) {
	require.True(t, IsRegistrationEmailSuffixAllowed("user@example.com", []string{"@example.com"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@example.com.", []string{"@example.com"}))
	require.False(t, IsRegistrationEmailSuffixAllowed("user@sub.example.com", []string{"@example.com"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@qq.com", []string{"@qq.com"}))
	require.False(t, IsRegistrationEmailSuffixAllowed("user@sub.qq.com", []string{"@qq.com"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("student@cs.edu.cn", []string{"*.edu.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("student@edu.cn", []string{"*.edu.cn"}))
	require.False(t, IsRegistrationEmailSuffixAllowed("student@foo.cn", []string{"*.edu.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@a.com", []string{"@a.com", "*.b.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@school.b.cn", []string{"@a.com", "*.b.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@b.cn", []string{"@a.com", "*.b.cn"}))
	require.False(t, IsRegistrationEmailSuffixAllowed("user@c.cn", []string{"@a.com", "*.b.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@any.com", []string{}))
}

func TestHasNonStandardEmailLocalPart(t *testing.T) {
	standard := []string{
		"user@example.com",
		"First.Last@Gmail.com",
		"a.b.c.d.e@gmail.com",
		"user+tag@gmail.com",
		"user_name@qq.com",
		"user-name@163.com",
		"_user@qq.com",
		"1234567890@qq.com",
		"a@qq.com",
		strings.Repeat("a", 64) + "@qq.com",
		// 缺少本地部分或域名的输入交给既有格式校验，不由本规则判定。
		"not-an-email",
		"@gmail.com",
		"user@",
	}
	for _, email := range standard {
		require.Falsef(t, HasNonStandardEmailLocalPart(email), "want standard: %s", email)
	}

	nonStandard := []string{
		`"a.b.c"@gmail.com`,
		`"ab"@gmail.com`,
		`"a@b"@gmail.com`,
		"a b@gmail.com",
		"a..b@gmail.com",
		".ab@gmail.com",
		"ab.@gmail.com",
		"+alice@gmail.com",
		"-ab@gmail.com",
		"ab-@gmail.com",
		"a!b@gmail.com",
		"a#b@gmail.com",
		"o'brien@gmail.com",
		`a\b@gmail.com`,
		"用户@qq.com",
		strings.Repeat("a", 65) + "@qq.com",
	}
	for _, email := range nonStandard {
		require.Truef(t, HasNonStandardEmailLocalPart(email), "want non-standard: %s", email)
	}
}

// 引号写法在发码前就被拒绝，不查库也不发信；注册与第三方邮箱策略同样拒绝。
func TestAuthService_RejectsNonStandardEmailLocalPart(t *testing.T) {
	settings := map[string]string{
		SettingKeyRegistrationEnabled:              "true",
		SettingKeyRegistrationEmailSuffixWhitelist: `["@gmail.com"]`,
	}
	ctx := context.Background()
	const quoted = `"a.b.c"@gmail.com`

	svc := newAuthService(&userRepoStub{}, settings, nil, nil)
	require.ErrorIs(t, svc.SendVerifyCode(ctx, quoted), ErrEmailFormatNotAllowed)
	_, err := svc.SendVerifyCodeAsync(ctx, quoted)
	require.ErrorIs(t, err, ErrEmailFormatNotAllowed)
	_, _, err = svc.Register(ctx, quoted, "password")
	require.ErrorIs(t, err, ErrEmailFormatNotAllowed)
	require.ErrorIs(t, svc.validateRegistrationEmailPolicy(ctx, quoted), ErrEmailFormatNotAllowed)
	require.Equal(t, "EMAIL_FORMAT_NOT_ALLOWED", infraerrors.FromError(ErrEmailFormatNotAllowed).Reason)

	// 正常写法不受影响。
	require.NoError(t, svc.validateRegistrationEmailPolicy(ctx, "a.b.c@gmail.com"))
}

func TestRegistrationEmailQuotaRejectsMalformedDomainWhenWhitelistConfigured(t *testing.T) {
	repo := &userRepoStub{}
	svc := newAuthService(repo, map[string]string{
		SettingKeyRegistrationEnabled:                 "true",
		SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com"]`,
		SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
	}, nil, nil)

	_, _, err := svc.Register(context.Background(), "malformed-email", "password")

	require.ErrorIs(t, err, ErrEmailSuffixNotAllowed)
	require.Empty(t, repo.created)
}

func TestIsRegistrationEmailSuffixLimited(t *testing.T) {
	require.False(t, IsRegistrationEmailSuffixLimited("user@custom.example", nil))
	require.False(t, IsRegistrationEmailSuffixLimited("user@example.com", []string{"@example.com"}))
	require.True(t, IsRegistrationEmailSuffixLimited("user@custom.example", []string{"@example.com"}))
}

func TestRegistrationEmailDomainUsesRegistrableDomain(t *testing.T) {
	require.Equal(t, "abc.com", RegistrationEmailDomain("user@abc.com"))
	require.Equal(t, "abc.com", RegistrationEmailDomain("user@abcd.abc.com"))
	require.Equal(t, "example.co.uk", RegistrationEmailDomain("user@team.example.co.uk"))
	require.Equal(t, "example.com", RegistrationEmailDomain("user@example.com."))
	require.Equal(t, "example.com", RegistrationEmailDomain("user@team.example.com."))
}
