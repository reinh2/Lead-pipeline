package autoreply

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/smtp"

	"github.com/anker-relocation/pipeline/pkg/models"
	"github.com/anker-relocation/pipeline/pkg/notify"
)

// Replier sends the AI-drafted first reply to the customer through whatever
// channel matches the lead's source. It never returns fatal errors — any
// failure is logged and the lead continues down the pipeline.
type Replier struct {
	Log *slog.Logger
	TG  *notify.Telegram // optional; used for source=telegram
	SMTP *SMTPConfig     // optional; when nil, email replies are logged
}

type SMTPConfig struct {
	Host, User, Pass, From string
	Port                   int
}

func (r *Replier) Reply(ctx context.Context, l models.Lead, q models.Qualification) {
	if q.Reply == "" {
		// Spam or empty — nothing to send.
		return
	}
	switch l.Source {
	case models.SourceTelegram:
		if r.TG != nil && l.TelegramChatID != 0 {
			if err := r.TG.SendDM(ctx, l.TelegramChatID, q.Reply); err != nil {
				r.Log.Warn("tg autoreply failed", "lead_id", l.ID, "err", err)
			}
			return
		}
		r.Log.Info("autoreply (tg demo)", "lead_id", l.ID, "text", q.Reply)
	default:
		if r.SMTP != nil && r.SMTP.Host != "" && l.Email != "" {
			if err := r.sendEmail(l, q); err != nil {
				r.Log.Warn("smtp autoreply failed", "lead_id", l.ID, "err", err)
			}
			return
		}
		r.Log.Info("autoreply (email demo)", "lead_id", l.ID, "to", l.Email, "text", q.Reply)
	}
}

func (r *Replier) sendEmail(l models.Lead, q models.Qualification) error {
	c := r.SMTP
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	auth := smtp.PlainAuth("", c.User, c.Pass, c.Host)
	subj := "Anker Relocation — your enquiry"
	msg := []byte("From: " + c.From + "\r\nTo: " + l.Email + "\r\nSubject: " + subj +
		"\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + q.Reply + "\r\n")
	// Use TLS if the standard submission port is 465; for 587 rely on SMTP's
	// STARTTLS negotiation inside smtp.SendMail.
	if c.Port == 465 {
		tconn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: c.Host})
		if err != nil {
			return err
		}
		client, err := smtp.NewClient(tconn, c.Host)
		if err != nil {
			return err
		}
		defer client.Quit()
		if err := client.Auth(auth); err != nil {
			return err
		}
		if err := client.Mail(c.From); err != nil {
			return err
		}
		if err := client.Rcpt(l.Email); err != nil {
			return err
		}
		w, err := client.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write(msg); err != nil {
			return err
		}
		return w.Close()
	}
	return smtp.SendMail(addr, auth, c.From, []string{l.Email}, msg)
}
