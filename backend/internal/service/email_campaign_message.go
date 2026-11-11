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
		return smtpMessage{}, smtpFailure("recipient", e, false)
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
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nDate: %s\r\nMessage-ID: %s\r\nSubject: %s\r\n", from.String(), (&mail.Address{Name: recipient.Name, Address: recipient.Address}).String(), time.Now().UTC().Format(time.RFC1123Z), id, foldCampaignSubject(r.Subject))
	fmt.Fprintf(&message, "List-Unsubscribe: <%s>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", r.UnsubscribeURL, multi.Boundary())
	_, _ = message.Write(body.Bytes())
	return smtpMessage{from.Address, recipient.Address, message.Bytes()}, nil
}

// QEncoding emits <=75-byte encoded words. Put each on its own continuation
// line so even a 200-rune CJK subject stays below the RFC 5322 recommendation.
func foldCampaignSubject(subject string) string {
	encoded := mime.QEncoding.Encode("UTF-8", sanitizeEmailHeader(subject))
	if strings.HasPrefix(encoded, "=?UTF-8?q?") {
		return "\r\n " + strings.ReplaceAll(encoded, "?= =?", "?=\r\n =?")
	}
	// ASCII subjects can be folded at existing spaces without changing their value.
	words := strings.Split(encoded, " ")
	var out strings.Builder
	column := len("Subject: ")
	for i, word := range words {
		if i > 0 {
			if column+1+len(word) > 78 {
				_, _ = out.WriteString("\r\n ")
				column = 1
			} else {
				_ = out.WriteByte(' ')
				column++
			}
		}
		_, _ = out.WriteString(word)
		column += len(word)
	}
	return out.String()
}
