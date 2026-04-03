package email

import (
	"bytes"
	"fmt"
	"io"
	"mime/quotedprintable"
	stdmail "net/mail"
	"net/smtp"
	"strings"
	"time"

	msgmail "github.com/emersion/go-message/mail"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func parseIncomingMessage(uid uint32, raw []byte) (incomingMessage, error) {
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return incomingMessage{}, fmt.Errorf("parse message: %w", err)
	}

	fromList, err := message.Header.AddressList("From")
	if err != nil || len(fromList) == 0 {
		return incomingMessage{}, fmt.Errorf("email has no From header")
	}

	from := fromList[0]
	subject := decodeHeader(message.Header.Get("Subject"))
	messageID := strings.TrimSpace(message.Header.Get("Message-Id"))
	if messageID == "" {
		messageID = fmt.Sprintf("<uid-%d>", uid)
	}

	date, _ := message.Header.Date()
	references := parseReferences(message.Header.Get("References"))
	if inReplyTo := strings.TrimSpace(message.Header.Get("In-Reply-To")); inReplyTo != "" {
		references = append(references, inReplyTo)
	}

	bodyText, err := extractBodyText(raw)
	if err != nil {
		return incomingMessage{}, err
	}
	bodyText = normalizeWhitespace(bodyText)
	rawHeader := formatFilterHeader(
		from,
		subject,
		date,
		messageID,
		message.Header.Get("Auto-Submitted"),
		message.Header.Get("Precedence"),
		message.Header.Get("List-Id"),
		message.Header.Get("X-Autoreply"),
	)

	return incomingMessage{
		UID:        uid,
		MessageID:  messageID,
		ChatID:     strings.ToLower(from.Address),
		SenderName: strings.TrimSpace(from.Name),
		Subject:    subject,
		Content:    buildInboundContent(from, subject, date, bodyText),
		Date:       date,
		References: references,
		RawHeader:  rawHeader,
	}, nil
}

func extractBodyText(raw []byte) (string, error) {
	reader, err := msgmail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("parse MIME body: %w", err)
	}

	var plainParts []string
	var htmlParts []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		inline, ok := part.Header.(*msgmail.InlineHeader)
		if !ok {
			continue
		}
		mediaType, _, _ := inline.ContentType()
		body, err := io.ReadAll(part.Body)
		if err != nil {
			return "", err
		}
		switch strings.ToLower(mediaType) {
		case "text/plain":
			plainParts = append(plainParts, string(body))
		case "text/html":
			htmlParts = append(htmlParts, htmlToText(string(body)))
		}
	}

	if len(plainParts) > 0 {
		return strings.Join(plainParts, "\n\n"), nil
	}
	if len(htmlParts) > 0 {
		return strings.Join(htmlParts, "\n\n"), nil
	}

	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	body, err := io.ReadAll(message.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (c *EmailChannel) shouldIgnoreMessage(msg incomingMessage) bool {
	if msg.ChatID == "" {
		return true
	}
	if strings.EqualFold(msg.ChatID, c.config.Address) || strings.EqualFold(msg.ChatID, c.config.Username) {
		return true
	}

	localPart := strings.ToLower(strings.SplitN(msg.ChatID, "@", 2)[0])
	if strings.Contains(localPart, "noreply") || strings.Contains(localPart, "no-reply") ||
		strings.Contains(localPart, "mailer-daemon") || strings.Contains(localPart, "postmaster") {
		return true
	}

	header := strings.ToLower(msg.RawHeader)
	if strings.Contains(header, "auto-submitted: auto-") ||
		strings.Contains(header, "precedence: bulk") ||
		strings.Contains(header, "precedence: junk") ||
		strings.Contains(header, "precedence: list") ||
		strings.Contains(header, "list-id:") ||
		strings.Contains(header, "x-autoreply:") {
		return true
	}
	return false
}

func (c *EmailChannel) sendSMTPMessage(to string, entry trackedEmail, msg bus.OutboundMessage) (string, error) {
	messageID, raw := c.buildMessage(to, entry, msg)
	if err := c.sendSMTP(to, raw); err != nil {
		return "", err
	}
	return messageID, nil
}

func (c *EmailChannel) buildMessage(to string, entry trackedEmail, msg bus.OutboundMessage) (string, []byte) {
	subject := replySubject(entry.Subject)
	messageID := c.nextSMTPMessageID()
	references := append([]string{}, entry.References...)
	if msg.ReplyToMessageID != "" && !containsString(references, msg.ReplyToMessageID) {
		references = append(references, msg.ReplyToMessageID)
	}

	var out bytes.Buffer
	out.WriteString(fmt.Sprintf("From: %s\r\n", formatAddress(c.config.Address)))
	out.WriteString(fmt.Sprintf("To: %s\r\n", formatAddress(to)))
	out.WriteString(fmt.Sprintf("Subject: %s\r\n", encodeHeader(subject)))
	out.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	out.WriteString(fmt.Sprintf("Message-ID: %s\r\n", messageID))
	if msg.ReplyToMessageID != "" {
		out.WriteString(fmt.Sprintf("In-Reply-To: %s\r\n", msg.ReplyToMessageID))
	}
	if len(references) > 0 {
		out.WriteString(fmt.Sprintf("References: %s\r\n", strings.Join(references, " ")))
	}
	out.WriteString("MIME-Version: 1.0\r\n")
	out.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	out.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	out.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&out)
	_, _ = qp.Write([]byte(strings.TrimSpace(msg.Content) + "\r\n"))
	_ = qp.Close()
	return messageID, out.Bytes()
}

func (c *EmailChannel) sendSMTP(to string, raw []byte) error {
	addr := fmt.Sprintf("%s:%d", c.config.SMTPHost, c.config.SMTPPort)
	auth := smtp.PlainAuth("", c.config.Username, c.config.Password, c.config.SMTPHost)

	if !c.config.SMTPTLS {
		return smtp.SendMail(addr, auth, c.config.Address, []string{to}, raw)
	}

	client, err := c.dialSMTP(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	if ok, _ := client.Extension("AUTH"); ok {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth failed: %w", err)
		}
	}
	if err := client.Mail(c.config.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM failed: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO failed: %w", err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA failed: %w", err)
	}
	if _, err := writer.Write(raw); err != nil {
		_ = writer.Close()
		return fmt.Errorf("smtp write failed: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp finalize failed: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit failed: %w", err)
	}
	return nil
}
