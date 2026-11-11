package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Each wake claims at most one recipient. PostgreSQL owns coordination across instances.
type EmailCampaignDispatcher struct {
	service *EmailCampaignService
	once    sync.Once
	cancel  context.CancelFunc
	done    chan struct{}
}

func ProvideEmailCampaignDispatcher(s *EmailCampaignService) *EmailCampaignDispatcher {
	d := &EmailCampaignDispatcher{service: s}
	d.Start()
	return d
}
func (d *EmailCampaignDispatcher) Start() {
	if d == nil || d.service == nil {
		return
	}
	d.once.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		d.cancel = cancel
		d.done = make(chan struct{})
		go d.run(ctx)
	})
}
func (d *EmailCampaignDispatcher) Stop() {
	if d == nil || d.cancel == nil {
		return
	}
	d.cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-d.done:
	case <-timer.C:
	}
}
func (d *EmailCampaignDispatcher) run(ctx context.Context) {
	defer close(d.done)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			active, e := d.dispatch(ctx)
			if e != nil && ctx.Err() == nil {
				slog.Warn("email campaign dispatch failed", "error", e)
			}
			delay := 10 * time.Second
			if active {
				delay = time.Second
			}
			timer.Reset(delay)
		}
	}
}
func (d *EmailCampaignDispatcher) dispatch(ctx context.Context) (bool, error) {
	s := d.service
	claim, e := s.repo.Claim(ctx)
	if e != nil || claim == nil {
		return false, e
	}
	r := claim.Recipient
	username, reason, e := s.repo.Eligible(ctx, r)
	if e != nil {
		return true, s.repo.Finish(ctx, r, EmailCampaignOutcome{Kind: "campaign", Error: e.Error()})
	}
	if reason != "" {
		return true, s.repo.Finish(ctx, r, EmailCampaignOutcome{Kind: "skipped", Error: reason})
	}
	rendered, e := s.Render(ctx, EmailCampaignContent{claim.Campaign.Subject, claim.Campaign.BodyMarkdown}, r.Email, username, claim.Campaign.ID)
	var outcome EmailCampaignOutcome
	if e == nil {
		var cfg *SMTPConfig
		cfg, e = s.email.GetSMTPConfig(ctx)
		if e == nil {
			var msg smtpMessage
			msg, e = buildCampaignSMTPMessage(cfg, r.Email, rendered)
			if e == nil {
				e = s.email.sendSMTPMessage(ctx, cfg, msg)
			}
		}
	}
	if e == nil {
		outcome.Kind = "sent"
	} else {
		outcome = classifyCampaignSMTP(e)
	}
	// Shutdown may happen after DATA: leave the claim for conservative stale recovery.
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	return true, s.repo.Finish(ctx, r, outcome)
}
func classifyCampaignSMTP(err error) EmailCampaignOutcome {
	if err == nil {
		return EmailCampaignOutcome{Kind: "sent"}
	}
	o := EmailCampaignOutcome{Kind: "campaign", Error: err.Error()}
	var delivery *SMTPDeliveryError
	if !errors.As(err, &delivery) {
		return o
	}
	o.Error = fmt.Sprintf("%s (SMTP %d): %s", delivery.Stage, delivery.Code, delivery.Err)
	if !delivery.DefinitelyNotDelivered {
		o.Kind = "uncertain"
		o.Error = "可能已送达：" + o.Error
		return o
	}
	if delivery.Stage == "rcpt" && (delivery.Code >= 500 && delivery.Code < 600 || delivery.Code >= 400 && delivery.Code < 500 && delivery.Code != 421) {
		o.Kind = "recipient"
	}
	o.Auth = delivery.Stage == "auth" && delivery.Code == 535
	return o
}
