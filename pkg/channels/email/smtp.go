package email

import (
	"crypto/tls"
	"fmt"
	"net/smtp"

	"github.com/google/uuid"
)

func (c *EmailChannel) dialSMTP(addr string) (*smtp.Client, error) {
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		ServerName: c.config.SMTPHost,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return nil, fmt.Errorf("smtp dial failed: %w", err)
	}

	client, err := smtp.NewClient(conn, c.config.SMTPHost)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smtp client failed: %w", err)
	}
	return client, nil
}

func (c *EmailChannel) nextSMTPMessageID() string {
	domain := domainFromAddress(c.config.Address)
	if domain == "" {
		domain = domainFromAddress(c.config.Username)
	}
	return fmt.Sprintf("<%s@%s>", uuid.NewString(), domain)
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
