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
	service        *EmailCampaignService
	once           sync.Once
	cancelClaims   context.CancelFunc
	cancelDelivery context.CancelFunc
	done           chan struct{}
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
		claims, cancelClaims := context.WithCancel(context.Background())
		delivery, cancelDelivery := context.WithCancel(context.Background())
		d.cancelClaims, d.cancelDelivery = cancelClaims, cancelDelivery
		d.done = make(chan struct{})
		go d.run(claims, delivery)
	})
}
func (d *EmailCampaignDispatcher) Stop() {
	if d == nil || d.cancelClaims == nil {
		return
	}
	// Stop claiming immediately, but let the in-flight message finish first.
	d.cancelClaims()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	defer d.cancelDelivery()
	select {
	case <-d.done:
	case <-timer.C:
		d.cancelDelivery()
		// Keep storage alive until the detached result write completes. SMTP has
		// only the two-second grace period; persistence has its own short budget.
		writeTimer := time.NewTimer(2 * time.Second)
		defer writeTimer.Stop()
		select {
		case <-d.done:
		case <-writeTimer.C:
		}
	}
}
func (d *EmailCampaignDispatcher) run(claims, delivery context.Context) {
	defer close(d.done)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-claims.Done():
			return
		case <-timer.C:
			active, err := d.dispatch(claims, delivery)
			if err != nil && claims.Err() == nil {
				slog.Warn("email campaign dispatch failed", "error", err)
			}
			delay := 10 * time.Second
			if active {
				delay = time.Second
			}
			timer.Reset(delay)
		}
	}
}
func (d *EmailCampaignDispatcher) finish(ctx context.Context, r EmailCampaignRecipient, outcome EmailCampaignOutcome) error {
	// A canceled SMTP/claim context must not strand a known delivery result.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return d.service.repo.Finish(writeCtx, r, outcome)
}
func (d *EmailCampaignDispatcher) dispatch(claims, delivery context.Context) (bool, error) {
	if err := claims.Err(); err != nil {
		return false, err
	}
	s := d.service
	claim, err := s.repo.Claim(claims)
	if err != nil || claim == nil {
		return false, err
	}
	r := claim.Recipient
	if err = claims.Err(); err != nil {
		return true, d.finish(delivery, r, EmailCampaignOutcome{Kind: "retry", Error: "发送前停止"})
	}
	if r.ClaimedAt == nil {
		return true, nil
	}
	ctx, cancel := context.WithDeadline(delivery, r.ClaimedAt.Add(EmailCampaignClaimLease))
	defer cancel()
	username, reason, err := s.repo.Eligible(ctx, r)
	if err != nil {
		return true, d.finish(ctx, r, campaignDeliveryOutcome(ctx, err))
	}
	if reason != "" {
		return true, d.finish(ctx, r, EmailCampaignOutcome{Kind: "skipped", Error: reason})
	}
	rendered, err := s.Render(ctx, EmailCampaignContent{claim.Campaign.Subject, claim.Campaign.BodyMarkdown}, r.Email, username, claim.Campaign.ID)
	if err == nil {
		var cfg *SMTPConfig
		cfg, err = s.email.GetSMTPConfig(ctx)
		if err == nil {
			var msg smtpMessage
			msg, err = buildCampaignSMTPMessage(cfg, r.Email, rendered)
			if err == nil {
				// Recheck the database generation and lease immediately before opening SMTP.
				var valid bool
				valid, err = s.repo.ClaimValid(ctx, r)
				if err == nil && !valid {
					return true, nil
				}
				if err == nil {
					err = s.sendSMTP(ctx, cfg, msg)
				}
			}
		}
	}
	outcome := campaignDeliveryOutcome(ctx, err)
	if ctx.Err() != nil && outcome.Kind == "uncertain" {
		// DATA may already have been committed; recovery will record that uncertainty.
		return true, ctx.Err()
	}
	return true, d.finish(ctx, r, outcome)
}
func campaignDeliveryOutcome(ctx context.Context, err error) EmailCampaignOutcome {
	outcome := classifyCampaignSMTP(err)
	if err != nil && outcome.Kind != "uncertain" && (ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return EmailCampaignOutcome{Kind: "retry", Error: "发送取消，确定未送达"}
	}
	return outcome
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
	if delivery.Stage == "recipient" || delivery.Stage == "rcpt" && (delivery.Code >= 500 && delivery.Code < 600 || delivery.Code >= 400 && delivery.Code < 500 && delivery.Code != 421) {
		o.Kind = "recipient"
		o.CountFailure = delivery.Stage == "rcpt" && delivery.Code >= 400 && delivery.Code < 500
	}
	o.Auth = delivery.Stage == "auth" && delivery.Code == 535
	return o
}
