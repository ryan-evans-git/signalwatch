// Package smtp delivers signalwatch notifications via SMTP.
package smtp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
)

// Config configures the SMTP channel.
type Config struct {
	Name     string
	Host     string
	Port     int
	Username string
	Password string
	From     string
	UseTLS   bool
}

type Channel struct {
	cfg Config
}

func New(cfg Config) *Channel {
	if cfg.Name == "" {
		cfg.Name = "smtp"
	}
	return &Channel{cfg: cfg}
}

func (c *Channel) Name() string { return c.cfg.Name }

func (c *Channel) Send(ctx context.Context, n channel.Notification) error {
	if n.Address == "" {
		return fmt.Errorf("smtp: empty address")
	}
	subject := fmt.Sprintf("[%s] %s: %s", strings.ToUpper(n.Kind), strings.ToUpper(n.Severity), n.RuleName)
	body := fmt.Sprintf(
		"Rule:        %s\nSeverity:    %s\nState:       %s\nValue:       %s\nIncident:    %s\nTriggered:   %s\n\n%s\n",
		n.RuleName, n.Severity, n.Kind, n.Value, n.IncidentID,
		n.TriggeredAt.UTC().Format("2006-01-02T15:04:05Z"),
		n.Description,
	)
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		c.cfg.From, n.Address, subject, body))

	addr := net.JoinHostPort(c.cfg.Host, fmt.Sprintf("%d", c.cfg.Port))

	var auth smtp.Auth
	if c.cfg.Username != "" {
		auth = smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)
	}

	if c.cfg.UseTLS {
		return sendTLS(addr, c.cfg.Host, auth, c.cfg.From, []string{n.Address}, msg)
	}
	return smtp.SendMail(addr, auth, c.cfg.From, []string{n.Address}, msg)
}

func sendTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	defer conn.Close()
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer func() { _ = client.Quit() }()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := client.Rcpt(addr); err != nil {
			return err
		}
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		return err
	}
	return wc.Close()
}
