package autoreply

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"time"

	"github.com/anker-relocation/pipeline/pkg/models"
	"github.com/anker-relocation/pipeline/pkg/notify"
)

// Replier sends the AI-drafted first reply to the customer through whatever
// channel matches the lead's source. Errors are returned so callers can apply
// the same bounded retry and dead-letter policy as other integrations.
type Replier struct {
	Log  *slog.Logger
	TG   *notify.Telegram // optional; used for source=telegram
	SMTP *SMTPConfig      // optional; when nil, email replies are logged
}

type SMTPConfig struct {
	Host, User, Pass, From string
	Port                   int
}

func (r *Replier) Reply(ctx context.Context, l models.Lead, q models.Qualification) error {
	if q.Reply == "" {
		// Spam or empty — nothing to send.
		return nil
	}
	switch l.Source {
	case models.SourceTelegram:
		if r.TG != nil && l.TelegramChatID != 0 {
			if err := r.TG.SendDM(ctx, l.TelegramChatID, q.Reply); err != nil {
				return fmt.Errorf("telegram autoreply: %w", err)
			}
			return nil
		}
		r.Log.Info("autoreply (tg demo)", "lead_id", l.ID)
		return nil
	default:
		if r.SMTP != nil && r.SMTP.Host != "" && l.Email != "" {
			if err := r.sendEmail(ctx, l, q); err != nil {
				return fmt.Errorf("smtp autoreply: %w", err)
			}
			return nil
		}
		r.Log.Info("autoreply (email demo)", "lead_id", l.ID)
		return nil
	}
}

func (r *Replier) sendEmail(ctx context.Context, l models.Lead, q models.Qualification) error {
	c := r.SMTP
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	auth := smtp.PlainAuth("", c.User, c.Pass, c.Host)
	subj := "Anker Relocation — your enquiry"
	msg := []byte("From: " + c.From + "\r\nTo: " + l.Email + "\r\nSubject: " + subj +
		"\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + q.Reply + "\r\n")

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if c.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig(c.Host)}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	} else if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}

	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return err
	}
	defer client.Quit()
	if c.Port != 465 {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("SMTP server does not support required STARTTLS")
		}
		if err := client.StartTLS(tlsConfig(c.Host)); err != nil {
			return err
		}
	}
	if c.User != "" && c.Pass != "" {
		if err := client.Auth(auth); err != nil {
			return err
		}
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

func tlsConfig(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}
