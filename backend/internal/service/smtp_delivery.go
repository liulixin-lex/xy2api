package service

import (
	"context"
	"errors"
	"fmt"
	"net/smtp"
	"net/textproto"
)

// SMTPDeliveryError distinguishes a rejection from an unknown DATA outcome.
type SMTPDeliveryError struct {
	Stage                  string
	Code                   int
	DefinitelyNotDelivered bool
	Err                    error
}

func (e *SMTPDeliveryError) Error() string { return fmt.Sprintf("smtp %s: %v", e.Stage, e.Err) }
func (e *SMTPDeliveryError) Unwrap() error { return e.Err }
func smtpFailure(stage string, err error, bodyStarted bool) error {
	var response *textproto.Error
	code := 0
	if errors.As(err, &response) {
		code = response.Code
	}
	return &SMTPDeliveryError{stage, code, !bodyStarted || code >= 400 && code < 600, err}
}
func (s *EmailService) sendSMTPMessage(ctx context.Context, config *SMTPConfig, message smtpMessage) error {
	if err := ctx.Err(); err != nil {
		return smtpFailure("connect", err, false)
	}
	client, err := s.connectSMTPContext(ctx, config)
	if err != nil {
		var staged *SMTPDeliveryError
		if errors.As(err, &staged) {
			return err
		}
		return smtpFailure("connect", err, false)
	}
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	return deliverSMTPMessage(client, config, message)
}

// deliverSMTPMessage also permits protocol verification over an in-memory connection.
func deliverSMTPMessage(client *smtp.Client, config *SMTPConfig, message smtpMessage) error {
	var err error
	defer func() { _ = client.Close() }()
	if err = client.Auth(smtp.PlainAuth("", config.Username, config.Password, config.Host)); err != nil {
		return smtpFailure("auth", err, false)
	}
	if err = client.Mail(message.envelopeFrom); err != nil {
		return smtpFailure("mail", err, false)
	}
	if err = client.Rcpt(message.envelopeTo); err != nil {
		return smtpFailure("rcpt", err, false)
	}
	w, err := client.Data()
	if err != nil {
		return smtpFailure("data", err, false)
	}
	if _, err = w.Write(message.data); err != nil {
		return smtpFailure("body", err, true)
	}
	if err = w.Close(); err != nil {
		return smtpFailure("commit", err, true)
	}
	// The server accepted DATA. QUIT failures cannot revoke that acceptance.
	_ = client.Quit()
	return nil
}
