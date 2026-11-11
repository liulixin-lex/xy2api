//go:build unit

package service

import (
	"bufio"
	"context"
	"errors"
	"html"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type campaignRevisionRepo struct {
	campaignTokenRepo
	claim          *EmailCampaignClaim
	valid          func(context.Context, EmailCampaignRecipient) (bool, error)
	eligible       func(context.Context) error
	outcome        *EmailCampaignOutcome
	finishErr      error
	finish         func(context.Context) error
	finishDeadline time.Time
	claims         int
	limit          string
}

func (r *campaignRevisionRepo) Claim(context.Context) (*EmailCampaignClaim, error) {
	r.claims++
	return r.claim, nil
}
func (r *campaignRevisionRepo) ClaimValid(ctx context.Context, recipient EmailCampaignRecipient) (bool, error) {
	if r.valid != nil {
		return r.valid(ctx, recipient)
	}
	return true, nil
}
func (r *campaignRevisionRepo) Eligible(ctx context.Context, _ EmailCampaignRecipient) (string, string, error) {
	if r.eligible != nil {
		return "", "", r.eligible(ctx)
	}
	return "user", "", nil
}
func (r *campaignRevisionRepo) Finish(ctx context.Context, _ EmailCampaignRecipient, outcome EmailCampaignOutcome) error {
	r.outcome = &outcome
	r.finishErr = ctx.Err()
	r.finishDeadline, _ = ctx.Deadline()
	if r.finish != nil {
		return r.finish(ctx)
	}
	return nil
}
func (r *campaignRevisionRepo) ReserveTestSend(context.Context) (string, error) { return r.limit, nil }

func campaignRevisionService(t *testing.T) (*EmailCampaignService, *campaignRevisionRepo) {
	t.Helper()
	settings := newNotificationEmailMemorySettingRepo()
	require.NoError(t, settings.SetMultiple(context.Background(), map[string]string{
		SettingKeySMTPHost: "localhost", SettingKeySMTPUsername: "user", SettingKeySMTPPassword: "secret-password",
		SettingKeySMTPFrom: "from@example.com", SettingKeyAPIBaseURL: "https://panel.example.com", SettingKeySiteName: `Site <&>`,
	}))
	now := time.Now()
	repo := &campaignRevisionRepo{campaignTokenRepo: campaignTokenRepo{secret: strings.Repeat("s", 64)}, claim: &EmailCampaignClaim{
		Campaign:  &EmailCampaign{ID: 1, Subject: "test", BodyMarkdown: "hello"},
		Recipient: EmailCampaignRecipient{CampaignID: 1, UserID: 2, Email: "to@example.com", ClaimVersion: 1, ClaimedAt: &now},
	}}
	email := NewEmailService(settings, nil)
	return NewEmailCampaignService(repo, settings, email, NewNotificationEmailService(settings, email), nil), repo
}

func TestEmailCampaignOldWorkerDoesNotSend(t *testing.T) {
	s, repo := campaignRevisionService(t)
	recovered, checked := false, false
	// The original worker resumes its preflight after another dispatcher has
	// recovered the row. Its retained recipient still has the old generation.
	repo.eligible = func(context.Context) error { recovered = true; return nil }
	repo.valid = func(ctx context.Context, r EmailCampaignRecipient) (bool, error) {
		checked = true
		require.True(t, recovered)
		require.EqualValues(t, 1, r.ClaimVersion)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Equal(t, r.ClaimedAt.Add(EmailCampaignClaimLease), deadline)
		return false, nil
	}
	s.sendSMTP = func(context.Context, *SMTPConfig, smtpMessage) error {
		t.Fatal("recovered worker opened SMTP")
		return nil
	}
	_, err := (&EmailCampaignDispatcher{service: s}).dispatch(context.Background(), context.Background())
	require.NoError(t, err)
	require.True(t, checked)
	require.Nil(t, repo.outcome)
}
func TestEmailCampaignCancellationResults(t *testing.T) {
	for _, tc := range []struct {
		name, stage, want   string
		accepted, uncertain bool
	}{
		{name: "before SMTP", want: "retry"}, {name: "during AUTH", stage: "auth", want: "retry"},
		{name: "partial body", stage: "body", want: "retry"}, {name: "lost DATA response", stage: "commit", uncertain: true},
		{name: "accepted then QUIT canceled", want: "sent", accepted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo := campaignRevisionService(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.stage == "" && !tc.accepted {
				repo.eligible = func(context.Context) error { cancel(); return context.Canceled }
				s.sendSMTP = func(context.Context, *SMTPConfig, smtpMessage) error { t.Fatal("unexpected SMTP"); return nil }
			} else {
				s.sendSMTP = func(context.Context, *SMTPConfig, smtpMessage) error {
					cancel()
					if tc.accepted {
						return nil
					}
					return smtpFailure(tc.stage, io.EOF, tc.uncertain)
				}
			}
			_, err := (&EmailCampaignDispatcher{service: s}).dispatch(context.Background(), ctx)
			if tc.uncertain {
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, repo.outcome)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, repo.outcome)
			require.Equal(t, tc.want, repo.outcome.Kind)
			require.NoError(t, repo.finishErr)
			require.WithinDuration(t, time.Now().Add(2*time.Second), repo.finishDeadline, time.Second)
		})
	}
}
func TestEmailCampaignStopGracePeriod(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain accepted", true: "cancel after budget"}[timeout], func(t *testing.T) {
			s, repo := campaignRevisionService(t)
			entered, release := make(chan struct{}), make(chan struct{})
			s.sendSMTP = func(ctx context.Context, _ *SMTPConfig, _ smtpMessage) error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return smtpFailure("body", ctx.Err(), false)
				}
			}
			d := &EmailCampaignDispatcher{service: s}
			d.Start()
			defer d.cancelDelivery()
			defer d.cancelClaims()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("dispatcher did not start")
			}
			stopped := make(chan struct{})
			start := time.Now()
			go func() { d.Stop(); close(stopped) }()
			if !timeout {
				select {
				case <-stopped:
					t.Fatal("Stop canceled in-flight mail before grace period")
				case <-time.After(30 * time.Millisecond):
				}
				close(release)
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("Stop exceeded budget")
			}
			if timeout {
				require.GreaterOrEqual(t, time.Since(start), 2*time.Second)
			}
			select {
			case <-d.done:
			case <-time.After(time.Second):
				t.Fatal("result was not written after cancellation")
			}
			require.Equal(t, 1, repo.claims)
			require.NoError(t, repo.finishErr)
			require.NotNil(t, repo.outcome)
			want := "sent"
			if timeout {
				want = "retry"
			}
			require.Equal(t, want, repo.outcome.Kind)
		})
	}
}
func TestEmailCampaignStopWaitsForResult(t *testing.T) {
	s, repo := campaignRevisionService(t)
	sending, writing, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.sendSMTP = func(ctx context.Context, _ *SMTPConfig, _ smtpMessage) error {
		close(sending)
		<-ctx.Done()
		return smtpFailure("auth", ctx.Err(), false)
	}
	repo.finish = func(ctx context.Context) error {
		close(writing)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d := &EmailCampaignDispatcher{service: s}
	d.Start()
	defer d.cancelDelivery()
	defer d.cancelClaims()
	select {
	case <-sending:
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP did not start")
	}
	stopped := make(chan struct{})
	go func() { d.Stop(); close(stopped) }()
	select {
	case <-writing:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation result not written")
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned before result persistence")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after write")
	}
	select {
	case <-d.done:
	default:
		t.Fatal("worker still running after Stop")
	}
	require.NoError(t, repo.finishErr)
	require.Equal(t, "retry", repo.outcome.Kind)
}

func TestEmailCampaignRecipientParseFailure(t *testing.T) {
	s, repo := campaignRevisionService(t)
	repo.claim.Recipient.Email = "a..b@x.com"
	s.sendSMTP = func(context.Context, *SMTPConfig, smtpMessage) error {
		t.Fatal("invalid recipient opened SMTP")
		return nil
	}
	_, err := (&EmailCampaignDispatcher{service: s}).dispatch(context.Background(), context.Background())
	require.NoError(t, err)
	require.Equal(t, "recipient", repo.outcome.Kind)
	require.False(t, repo.outcome.CountFailure)
	require.Contains(t, repo.outcome.Error, "recipient")
}
func TestEmailCampaignPermanentAndTemporaryRCPT(t *testing.T) {
	for _, code := range []int{450, 550} {
		outcome := classifyCampaignSMTP(smtpFailure("rcpt", &textproto.Error{Code: code, Msg: "refused"}, false))
		require.Equal(t, "recipient", outcome.Kind)
		require.Equal(t, code == 450, outcome.CountFailure)
	}
}
func TestEmailCampaignLongSubject(t *testing.T) {
	subject := strings.Repeat("中", 200)
	msg, err := buildCampaignSMTPMessage(&SMTPConfig{From: "from@example.com", Host: "example.com"}, "to@example.com", &EmailCampaignRendered{Subject: subject, Text: "hello", HTML: "<p>hello</p>", UnsubscribeURL: "https://example.com/unsubscribe"})
	require.NoError(t, err)
	headers := strings.SplitN(string(msg.data), "\r\n\r\n", 2)[0]
	inSubject := false
	for _, line := range strings.Split(headers, "\r\n") {
		require.LessOrEqual(t, len(line), 998)
		if strings.HasPrefix(line, "Subject:") {
			inSubject = true
		} else if !strings.HasPrefix(line, " ") {
			inSubject = false
		}
		if inSubject {
			require.LessOrEqual(t, len(line), 78)
		}
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(msg.data)))
	require.NoError(t, err)
	decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	require.NoError(t, err)
	require.Equal(t, subject, decoded)
}
func TestEmailCampaignLinkPlaceholders(t *testing.T) {
	s, _ := campaignRevisionService(t)
	username := `x &"<img src=x> [click](https://evil.test) {{site_name}}`
	r, err := s.Render(context.Background(), EmailCampaignContent{Subject: "test", BodyMarkdown: `[go](https://example.com/?u={{username}}&s={{site_name}}&v=%7b%7busername%7d%7d)`}, "to@example.com", username, 0)
	require.NoError(t, err)
	escaped := html.EscapeString(url.QueryEscape(username))
	require.Contains(t, r.HTML, "u="+escaped+"&amp;s="+html.EscapeString(url.QueryEscape(`Site <&>`))+"&amp;v="+escaped)
	require.NotContains(t, r.HTML, "<img")
	require.NotContains(t, r.HTML, `href="https://evil.test`)
}
func TestEmailCampaignTestSendErrors(t *testing.T) {
	for _, window := range []string{"minute", "day"} {
		s, repo := campaignRevisionService(t)
		repo.limit = window
		s.sendSMTP = func(context.Context, *SMTPConfig, smtpMessage) error {
			t.Fatal("rate-limited send opened SMTP")
			return nil
		}
		_, err := s.TestSend(context.Background(), EmailCampaignContent{Subject: "test", BodyMarkdown: "hello"}, "to@example.com")
		require.Equal(t, 429, infraerrors.Code(err))
		require.Equal(t, "EMAIL_CAMPAIGN_RATE_LIMITED", infraerrors.Reason(err))
		want := "每分钟"
		if window == "day" {
			want = "24 小时"
		}
		require.Contains(t, infraerrors.Message(err), want)
	}
	s, _ := campaignRevisionService(t)
	s.sendSMTP = func(context.Context, *SMTPConfig, smtpMessage) error {
		return smtpFailure("auth", &textproto.Error{Code: 535, Msg: "secret-password echoed"}, false)
	}
	_, err := s.TestSend(context.Background(), EmailCampaignContent{Subject: "test", BodyMarkdown: "hello"}, "to@example.com")
	require.Equal(t, 502, infraerrors.Code(err))
	require.Equal(t, "EMAIL_CAMPAIGN_SEND_FAILED", infraerrors.Reason(err))
	require.Contains(t, infraerrors.Message(err), "auth")
	require.Contains(t, infraerrors.Message(err), "535")
	require.NotContains(t, err.Error(), "secret-password")
}

// Exercise actual net/smtp DATA framing over net.Pipe, without a TCP listener.
func TestEmailCampaignSMTPPartialBodyAndQUIT(t *testing.T) {
	for _, partial := range []bool{true, false} {
		t.Run(map[bool]string{true: "write before terminator fails", false: "250 then QUIT disconnected"}[partial], func(t *testing.T) {
			sender, receiver := net.Pipe()
			defer func() { _ = sender.Close() }()
			require.NoError(t, sender.SetDeadline(time.Now().Add(3*time.Second)))
			done := make(chan error, 1)
			go func() {
				defer func() { _ = receiver.Close() }()
				proto := textproto.NewConn(receiver)
				if err := proto.PrintfLine("220 localhost ready"); err != nil {
					done <- err
					return
				}
				for {
					line, err := proto.ReadLine()
					if err != nil {
						done <- err
						return
					}
					response := "250 ok"
					switch {
					case strings.HasPrefix(line, "EHLO"):
						response = "250-localhost\r\n250 AUTH PLAIN"
					case strings.HasPrefix(line, "AUTH"):
						response = "235 ok"
					case line == "DATA":
						if err = proto.PrintfLine("354 send"); err != nil {
							done <- err
							return
						}
						if partial {
							_, err = bufio.NewReader(receiver).ReadByte()
							done <- err
							return
						}
						_, err = io.Copy(io.Discard, proto.DotReader())
						if err != nil {
							done <- err
							return
						}
					case line == "QUIT":
						done <- nil
						return
					}
					if err = proto.PrintfLine("%s", response); err != nil {
						done <- err
						return
					}
				}
			}()
			client, err := newSMTPClientContext(context.Background(), sender, "localhost")
			require.NoError(t, err)
			err = deliverSMTPMessage(client, &SMTPConfig{Host: "localhost", Username: "u", Password: "p"}, smtpMessage{"from@example.com", "to@example.com", []byte(strings.Repeat("body\r\n", 5000))})
			if partial {
				var delivery *SMTPDeliveryError
				require.True(t, errors.As(err, &delivery))
				require.Equal(t, "body", delivery.Stage)
				require.True(t, delivery.DefinitelyNotDelivered)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, <-done)
		})
	}
}
