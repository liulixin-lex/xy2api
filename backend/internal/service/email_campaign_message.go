package service

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
)

func buildCampaignSMTPMessage(config *SMTPConfig, to string, r *EmailCampaignRendered) (smtpMessage, error) {
	if config == nil || r == nil {
		return smtpMessage{}, ErrEmailCampaignInvalidInput
	}
	from, e := parseSMTPAddress(config.From, "from")
	if e != nil {
		return smtpMessage{}, e
	}
	recipient, e := parseSMTPAddress(to, "recipient")
	if e != nil {
		return smtpMessage{}, e
	}
	id, e := generateEmailMessageID(from.Address, config.Host)
	if e != nil {
		return smtpMessage{}, e
	}
	if strings.ContainsAny(r.UnsubscribeURL, "\r\n<>") {
		return smtpMessage{}, ErrEmailCampaignInvalidInput
	}
	if name := sanitizeEmailHeader(config.FromName); strings.TrimSpace(name) != "" {
		from.Name = name
	}
	var body bytes.Buffer
	multi := multipart.NewWriter(&body)
	for _, part := range []struct{ kind, body string }{{"text/plain", r.Text}, {"text/html", r.HTML}} {
		header := textproto.MIMEHeader{"Content-Type": {part.kind + "; charset=UTF-8"}, "Content-Transfer-Encoding": {"quoted-printable"}}
		writer, err := multi.CreatePart(header)
		if err != nil {
			return smtpMessage{}, err
		}
		qp := quotedprintable.NewWriter(writer)
		if _, err = qp.Write([]byte(part.body)); err != nil {
			return smtpMessage{}, err
		}
		if err = qp.Close(); err != nil {
			return smtpMessage{}, err
		}
	}
	if e = multi.Close(); e != nil {
		return smtpMessage{}, e
	}
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nDate: %s\r\nMessage-ID: %s\r\nSubject: %s\r\n", from.String(), (&mail.Address{Name: recipient.Name, Address: recipient.Address}).String(), time.Now().UTC().Format(time.RFC1123Z), id, mime.QEncoding.Encode("UTF-8", sanitizeEmailHeader(r.Subject)))
	fmt.Fprintf(&message, "List-Unsubscribe: <%s>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", r.UnsubscribeURL, multi.Boundary())
	_, _ = message.Write(body.Bytes())
	return smtpMessage{from.Address, recipient.Address, message.Bytes()}, nil
}
