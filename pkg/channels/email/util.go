package email

import (
	"fmt"
	"mime"
	stdmail "net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

var htmlTagRE = regexp.MustCompile(`(?s)<[^>]+>`)

func resolvePollInterval(explicitMinutes, heartbeatMinutes int) time.Duration {
	minutes := explicitMinutes
	if minutes == 0 {
		minutes = heartbeatMinutes
	}
	if minutes == 0 {
		return defaultPollInterval
	}
	if minutes < minPollIntervalMinutes {
		minutes = minPollIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func buildInboundContent(from *stdmail.Address, subject string, date time.Time, body string) string {
	var b strings.Builder
	b.WriteString("Email received.\n")
	b.WriteString(fmt.Sprintf("From: %s\n", from.String()))
	if subject != "" {
		b.WriteString(fmt.Sprintf("Subject: %s\n", subject))
	}
	if !date.IsZero() {
		b.WriteString(fmt.Sprintf("Date: %s\n", date.Format(time.RFC3339)))
	}
	b.WriteString("\nBody:\n")
	b.WriteString(strings.TrimSpace(body))
	return strings.TrimSpace(b.String())
}

func parseReferences(raw string) []string {
	fields := strings.Fields(raw)
	var refs []string
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			refs = append(refs, field)
		}
	}
	return refs
}

func htmlToText(html string) string {
	replaced := strings.NewReplacer(
		"<br>", "\n",
		"<br/>", "\n",
		"<br />", "\n",
		"</p>", "\n\n",
		"</div>", "\n",
	).Replace(html)
	return normalizeWhitespace(htmlTagRE.ReplaceAllString(replaced, " "))
}

func normalizeWhitespace(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(out) == 0 || out[len(out)-1] == "" {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func decodeHeader(s string) string {
	decoder := new(mime.WordDecoder)
	decoded, err := decoder.DecodeHeader(strings.TrimSpace(s))
	if err == nil {
		return decoded
	}
	return strings.TrimSpace(s)
}

func encodeHeader(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	return mime.QEncoding.Encode("utf-8", s)
}

func replySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "Re: your message"
	}
	lower := strings.ToLower(subject)
	if strings.HasPrefix(lower, "re:") {
		return subject
	}
	return "Re: " + subject
}

func formatAddress(address string) string {
	addr := stdmail.Address{Address: strings.TrimSpace(address)}
	return addr.String()
}

func formatEnvelope(from *stdmail.Address, subject string, date time.Time, messageID string) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("From: %s", from.String()))
	if subject != "" {
		lines = append(lines, fmt.Sprintf("Subject: %s", subject))
	}
	if !date.IsZero() {
		lines = append(lines, fmt.Sprintf("Date: %s", date.Format(time.RFC3339)))
	}
	if messageID != "" {
		lines = append(lines, fmt.Sprintf("Message-ID: %s", messageID))
	}
	return strings.Join(lines, "\n")
}

func formatFilterHeader(
	from *stdmail.Address,
	subject string,
	date time.Time,
	messageID string,
	autoSubmitted string,
	precedence string,
	listID string,
	xAutoReply string,
) string {
	lines := []string{formatEnvelope(from, subject, date, messageID)}
	appendIfPresent := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		lines = append(lines, fmt.Sprintf("%s: %s", label, value))
	}

	appendIfPresent("Auto-Submitted", autoSubmitted)
	appendIfPresent("Precedence", precedence)
	appendIfPresent("List-Id", listID)
	appendIfPresent("X-Autoreply", xAutoReply)

	return strings.Join(lines, "\n")
}

func domainFromAddress(address string) string {
	parts := strings.SplitN(strings.TrimSpace(address), "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func maxUID(uids []imap.UID) uint32 {
	var maxValue uint32
	for _, uid := range uids {
		if uint32(uid) > maxValue {
			maxValue = uint32(uid)
		}
	}
	return maxValue
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
