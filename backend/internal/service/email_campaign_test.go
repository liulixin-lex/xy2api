//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type campaignTokenRepo struct {
	EmailCampaignRepository
	secret string
}

func (r campaignTokenRepo) Secret(context.Context) (string, error)     { return r.secret, nil }
func (r campaignTokenRepo) ReadSecret(context.Context) (string, error) { return r.secret, nil }
func TestEmailCampaignUnsubscribeToken(t *testing.T) {
	ctx := context.Background()
	secret := strings.Repeat("a", 64)
	settings := newNotificationEmailMemorySettingRepo()
	require.NoError(t, settings.Set(ctx, notificationEmailUnsubscribeSecretKey, secret))
	notifications := NewNotificationEmailService(settings, nil)
	notificationToken, e := notifications.createUnsubscribeToken(ctx, "someone@example.com", NotificationEmailEventBalanceLow)
	require.NoError(t, e)
	token := campaignToken(secret, " Someone@Example.com ", 7)
	for _, tc := range []struct {
		name, token string
		ok          bool
	}{{"round trip", token, true}, {"tampered", token + "a", false}, {"notification namespace", notificationToken, false}, {"empty", "", false}, {"wrong key", campaignToken("other", "someone@example.com", 7), false}} {
		t.Run(tc.name, func(t *testing.T) {
			v, e := parseCampaignToken(secret, tc.token)
			if tc.ok {
				require.NoError(t, e)
				require.Equal(t, "someone@example.com", v.Email)
				require.EqualValues(t, 7, v.CampaignID)
			} else {
				require.Error(t, e)
			}
		})
	}
	_, e = notifications.parseUnsubscribeToken(ctx, token)
	require.Error(t, e)
}
func TestEmailCampaignSMTPClassification(t *testing.T) {
	for _, tc := range []struct {
		stage string
		code  int
		body  bool
		kind  string
		auth  bool
	}{
		{"connect", 0, false, "campaign", false}, {"tls", 0, false, "campaign", false}, {"auth", 535, false, "campaign", true}, {"mail", 550, false, "campaign", false},
		{"rcpt", 550, false, "recipient", false}, {"rcpt", 450, false, "recipient", false}, {"rcpt", 421, false, "campaign", false}, {"rcpt", 0, false, "campaign", false},
		{"data", 554, false, "campaign", false}, {"commit", 451, true, "campaign", false}, {"commit", 0, true, "uncertain", false}, {"body", 0, false, "campaign", false},
	} {
		t.Run(tc.stage+"/"+tc.kind, func(t *testing.T) {
			var err error = io.EOF
			if tc.code != 0 {
				err = &textproto.Error{Code: tc.code, Msg: "fixture rejection"}
			}
			got := classifyCampaignSMTP(smtpFailure(tc.stage, err, tc.body))
			require.Equal(t, tc.kind, got.Kind)
			require.Equal(t, tc.auth, got.Auth)
		})
	}
}
func TestEmailCampaignRendering(t *testing.T) {
	ctx := context.Background()
	settings := newNotificationEmailMemorySettingRepo()
	require.NoError(t, settings.Set(ctx, SettingKeyAPIBaseURL, "https://panel.example.com"))
	require.NoError(t, settings.Set(ctx, SettingKeySiteName, `Site <&>`))
	sender := NewEmailService(settings, nil)
	s := NewEmailCampaignService(campaignTokenRepo{secret: strings.Repeat("s", 64)}, settings, sender, NewNotificationEmailService(settings, sender), nil)
	c := EmailCampaignContent{"Hello {{username}}\r\nBcc: nobody", "# Welcome {{username}}\n\n<b>{{site_name}}</b>"}
	rendered, e := s.Render(ctx, c, "test@example.com", `<img src=x onerror="x">`, 0)
	require.NoError(t, e)
	require.NotContains(t, rendered.HTML, `<img src=x`)
	require.Contains(t, rendered.HTML, "&lt;img")
	require.Contains(t, rendered.HTML, "<b>Site &lt;&amp;&gt;</b>")
	require.Contains(t, rendered.Text, `<img src=x`)
	require.Contains(t, rendered.Text, "Unsubscribe")
	require.NotContains(t, rendered.Subject, "\r")
	require.NotContains(t, rendered.Subject, "\n")
	fallback, e := s.Render(ctx, EmailCampaignContent{"{{username}}", "{{username}}"}, "fallback@example.com", "", 0)
	require.NoError(t, e)
	require.Equal(t, "fallback", fallback.Subject)
	require.NoError(t, settings.Set(ctx, SettingKeyAPIBaseURL, "http://panel.example.com"))
	_, e = s.Render(ctx, c, "test@example.com", "", 0)
	require.ErrorIs(t, e, ErrEmailCampaignSiteURLRequired)
}
func TestEmailCampaignSMTPDelivery(t *testing.T) {
	ctx := context.Background()
	smtp := startNotificationEmailTestSMTPServer(t)
	settings := newNotificationEmailMemorySettingRepo()
	require.NoError(t, settings.SetMultiple(ctx, smtp.settings()))
	require.NoError(t, settings.Set(ctx, SettingKeyAPIBaseURL, "https://panel.example.com"))
	sender := NewEmailService(settings, nil)
	s := NewEmailCampaignService(campaignTokenRepo{secret: strings.Repeat("s", 64)}, settings, sender, NewNotificationEmailService(settings, sender), nil)
	rendered, e := s.Render(ctx, EmailCampaignContent{Subject: "Hello\r\nBcc: nobody", BodyMarkdown: "Hello"}, "test@example.com", "", 0)
	require.NoError(t, e)

	cfg, e := sender.GetSMTPConfig(ctx)
	require.NoError(t, e)
	msg, e := buildCampaignSMTPMessage(cfg, "test@example.com", rendered)
	require.NoError(t, e)
	require.NoError(t, sender.sendSMTPMessage(ctx, cfg, msg))
	message, e := mail.ReadMessage(strings.NewReader(smtp.lastMessage()))
	require.NoError(t, e)
	require.Equal(t, "<"+rendered.UnsubscribeURL+">", message.Header.Get("List-Unsubscribe"))
	require.Equal(t, "List-Unsubscribe=One-Click", message.Header.Get("List-Unsubscribe-Post"))
	require.Empty(t, message.Header.Get("Bcc"))
	kind, params, e := mime.ParseMediaType(message.Header.Get("Content-Type"))
	require.NoError(t, e)
	require.Equal(t, "multipart/alternative", kind)
	multi := multipart.NewReader(message.Body, params["boundary"])
	for _, want := range []string{"text/plain", "text/html"} {
		p, e := multi.NextPart()
		require.NoError(t, e)
		require.Contains(t, p.Header.Get("Content-Type"), want)
		b, e := io.ReadAll(p)
		require.NoError(t, e)
		require.Contains(t, string(b), rendered.UnsubscribeURL)
	}
	_, e = multi.NextPart()
	require.ErrorIs(t, e, io.EOF)
	smtp.dropAfterDATA.Store(true)
	e = sender.sendSMTPMessage(ctx, cfg, msg)
	require.Error(t, e)
	require.Equal(t, "uncertain", classifyCampaignSMTP(e).Kind)
}

func TestEmailCampaignAudienceValidation(t *testing.T) {
	date := func(v string) *string { return &v }
	days := func(v int) *int { return &v }
	for _, tc := range []struct {
		name string
		a    EmailCampaignAudience
		ok   bool
	}{
		{"valid DST", EmailCampaignAudience{Timezone: "America/New_York", RegisteredFrom: date("2026-03-08"), RegisteredTo: date("2026-03-08"), InactiveDays: days(30)}, true},
		{"timezone", EmailCampaignAudience{Timezone: "not/a-zone"}, false}, {"empty zone", EmailCampaignAudience{}, false}, {"Local zone", EmailCampaignAudience{Timezone: "Local"}, false},
		{"date", EmailCampaignAudience{Timezone: "UTC", RegisteredFrom: date("2026-02-30")}, false},
		{"reverse", EmailCampaignAudience{Timezone: "UTC", RegisteredFrom: date("2026-03-02"), RegisteredTo: date("2026-03-01")}, false},
		{"days zero", EmailCampaignAudience{Timezone: "UTC", InactiveDays: days(0)}, false}, {"days max", EmailCampaignAudience{Timezone: "UTC", InactiveDays: days(3651)}, false},
		{"exclude", EmailCampaignAudience{Timezone: "UTC", Exclude: make([]string, 1001)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, e := ValidateEmailCampaignAudience(tc.a, time.Now())
			if tc.ok {
				require.NoError(t, e)
				require.Equal(t, 23*time.Hour, q.To.Sub(*q.From))
			} else {
				require.ErrorIs(t, e, ErrEmailCampaignInvalidInput)
			}
		})
	}
}
func TestEmailCampaignSMTPShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := (&EmailService{}).sendSMTPMessage(ctx, &SMTPConfig{}, smtpMessage{})
	require.True(t, errors.Is(e, context.Canceled))
	var d *EmailCampaignDispatcher
	require.NotPanics(t, d.Stop)
}

func TestEmailCampaignSMTPInMemory(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "lost DATA response"}[drop], func(t *testing.T) {
			server := &notificationEmailTestSMTPServer{}
			server.dropAfterDATA.Store(drop)
			sender, receiver := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); server.handleConn(receiver) }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			stop := context.AfterFunc(ctx, func() { _ = sender.Close() })
			defer stop()
			client, e := newSMTPClientContext(context.Background(), sender, "localhost")
			require.NoError(t, e)
			cfg := &SMTPConfig{Host: "localhost", Username: "user", Password: "password", From: "from@example.com", FromName: "Site\r\nInjected"}
			msg, e := buildCampaignSMTPMessage(cfg, "to@example.com", &EmailCampaignRendered{Subject: "subject\r\nBcc: bad", Text: "plain", HTML: "<p>html</p>", UnsubscribeURL: "https://example.com/api/v1/email-campaigns/unsubscribe?token=test"})
			require.NoError(t, e)
			e = deliverSMTPMessage(client, cfg, msg)
			if drop {
				require.Error(t, e)
				require.Equal(t, "uncertain", classifyCampaignSMTP(e).Kind)
			} else {
				require.NoError(t, e)
			}
			<-done
			parsed, e := mail.ReadMessage(strings.NewReader(server.lastMessage()))
			require.NoError(t, e)
			require.Equal(t, "<https://example.com/api/v1/email-campaigns/unsubscribe?token=test>", parsed.Header.Get("List-Unsubscribe"))
			require.Equal(t, "List-Unsubscribe=One-Click", parsed.Header.Get("List-Unsubscribe-Post"))
			require.Empty(t, parsed.Header.Get("Bcc"))
			kind, params, e := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
			require.NoError(t, e)
			require.Equal(t, "multipart/alternative", kind)
			parts := multipart.NewReader(parsed.Body, params["boundary"])
			for _, want := range []string{"text/plain", "text/html"} {
				part, e := parts.NextPart()
				require.NoError(t, e)
				require.Contains(t, part.Header.Get("Content-Type"), want)
				_, e = io.ReadAll(part)
				require.NoError(t, e)
			}
			_, e = parts.NextPart()
			require.ErrorIs(t, e, io.EOF)
		})
	}
}

// A trailing dot resolves to localhost, but is deliberately not PlainAuth's
// localhost plaintext exception. Implicit TLS must remain visible to net/smtp.
func TestEmailCampaignSMTPImplicitTLSHost(t *testing.T) {
	_, port := startFakeSMTPServer(t, true, false)
	cfg := smtpTestConfig(port, true)
	cfg.Host = "localhost."
	require.NoError(t, (&EmailService{}).SendEmailWithConfig(cfg, "to@example.com", "Subject", "<p>Body</p>"))
}
