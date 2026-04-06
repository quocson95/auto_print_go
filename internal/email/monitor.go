package email

import (
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"golang.org/x/net/html"

	"github.com/sondq/auto_print/internal/config"
)

type Monitor struct {
	cfg *config.Config

	imapClient        *client.Client
	lastNoopTime      time.Time
	noopInterval      time.Duration
	connectionCreated time.Time
	maxConnectionAge  time.Duration
	processedUIDs     map[uint32]struct{}
}

type EmailResult struct {
	UID     uint32
	Subject string
	Link    string
}

func NewMonitor(cfg *config.Config) *Monitor {
	return &Monitor{
		cfg:              cfg,
		noopInterval:     60 * time.Second,
		maxConnectionAge: time.Hour,
		processedUIDs:    make(map[uint32]struct{}),
	}
}

func (m *Monitor) ensureConnection() error {
	now := time.Now()

	if m.imapClient != nil {
		if now.Sub(m.connectionCreated) > m.maxConnectionAge {
			slog.Info("Connection age exceeds max, reconnecting...")
			m.reconnect()
			return nil
		}
	}

	if m.imapClient == nil {
		return m.connect()
	}

	if now.Sub(m.lastNoopTime) > m.noopInterval {
		if err := m.imapClient.Noop(); err != nil {
			slog.Warn("NOOP failed, reconnecting...", "error", err)
			m.reconnect()
		}
		m.lastNoopTime = now
	}

	return nil
}

func (m *Monitor) connect() error {
	addr := fmt.Sprintf("%s:%d", m.cfg.IMAPServer, m.cfg.IMAPPort)
	slog.Info("Connecting to IMAP server", "addr", addr)

	c, err := client.DialTLS(addr, nil)
	if err != nil {
		return fmt.Errorf("dial TLS: %w", err)
	}

	if err := c.Login(m.cfg.EmailAddress, m.cfg.EmailPassword); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	if _, err := c.Select("INBOX", false); err != nil {
		return fmt.Errorf("select INBOX: %w", err)
	}

	m.imapClient = c
	m.connectionCreated = time.Now()
	m.lastNoopTime = m.connectionCreated
	slog.Info("IMAP connection established")
	return nil
}

func (m *Monitor) reconnect() {
	m.Disconnect()
	if err := m.connect(); err != nil {
		slog.Error("Reconnect failed", "error", err)
	}
}

func (m *Monitor) Disconnect() {
	if m.imapClient != nil {
		if err := m.imapClient.Logout(); err != nil {
			slog.Debug("Logout error", "error", err)
		}
		m.imapClient = nil
		slog.Info("IMAP connection closed")
	}
}

func (m *Monitor) MarkAsRead(uid uint32) {
	if err := m.ensureConnection(); err != nil {
		slog.Error("Failed to ensure connection for mark read", "error", err)
		return
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)
	flags := []interface{}{imap.SeenFlag}

	if err := m.imapClient.UidStore(seqSet, imap.FormatFlagsOp(imap.AddFlags, false), flags, nil); err != nil {
		slog.Error("Failed to mark email as read", "uid", uid, "error", err)
		m.reconnect()
		return
	}
	slog.Info("Marked email as read", "uid", uid)
}

func (m *Monitor) MarkAsNotRead(uid uint32) {
	if err := m.ensureConnection(); err != nil {
		slog.Error("Failed to ensure connection for mark unread", "error", err)
		return
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)
	flags := []interface{}{imap.SeenFlag}

	if err := m.imapClient.UidStore(seqSet, imap.FormatFlagsOp(imap.RemoveFlags, false), flags, nil); err != nil {
		slog.Error("Failed to mark email as not read", "uid", uid, "error", err)
		m.reconnect()
		return
	}
	slog.Info("Marked email as not read", "uid", uid)
}

func (m *Monitor) extractLink(body string) string {
	keyword := strings.ToLower(m.cfg.ViewBrowserKeyword)

	tokenizer := html.NewTokenizer(strings.NewReader(body))
	var inLink bool
	var href string

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return m.extractLinkRegex(body)

		case html.StartTagToken:
			t := tokenizer.Token()
			if t.Data == "a" {
				inLink = true
				for _, attr := range t.Attr {
					if attr.Key == "href" {
						href = attr.Val
					}
				}
			}

		case html.TextToken:
			if inLink {
				text := strings.TrimSpace(tokenizer.Token().Data)
				if strings.Contains(strings.ToLower(text), keyword) && href != "" {
					slog.Info("Found link", "keyword", m.cfg.ViewBrowserKeyword, "href", href)
					return href
				}
			}

		case html.EndTagToken:
			t := tokenizer.Token()
			if t.Data == "a" {
				inLink = false
				href = ""
			}
		}
	}
}

func (m *Monitor) extractLinkRegex(body string) string {
	pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(m.cfg.ViewBrowserKeyword) + `.*?(https?://[^\s<>"]+)`)
	match := pattern.FindStringSubmatch(body)
	if len(match) > 1 {
		slog.Info("Found URL via regex", "url", match[1])
		return match[1]
	}
	slog.Warn("No link found in email", "keyword", m.cfg.ViewBrowserKeyword)
	return ""
}

func getEmailBody(msg *mail.Message) string {
	contentType := msg.Header.Get("Content-Type")

	if !strings.Contains(contentType, "multipart") {
		b, err := io.ReadAll(msg.Body)
		if err != nil {
			slog.Error("Failed to read email body", "error", err)
			return ""
		}
		return string(b)
	}

	boundary := extractBoundary(contentType)
	if boundary == "" {
		b, err := io.ReadAll(msg.Body)
		if err != nil {
			return ""
		}
		return string(b)
	}

	return parseMultipart(msg.Body, boundary)
}

func extractBoundary(contentType string) string {
	for _, param := range strings.Split(contentType, ";") {
		param = strings.TrimSpace(param)
		lower := strings.ToLower(param)
		if strings.HasPrefix(lower, "boundary=") {
			b := param[len("boundary="):]
			b = strings.Trim(b, `"`)
			return b
		}
	}
	return ""
}

func parseMultipart(r io.Reader, boundary string) string {
	mr := multipart.NewReader(r, boundary)

	var htmlPart, textPart string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}

		ct := p.Header.Get("Content-Type")
		lower := strings.ToLower(ct)

		data, err := io.ReadAll(p)
		if err != nil {
			continue
		}

		if strings.Contains(lower, "multipart/") {
			nested := extractBoundary(ct)
			if nested != "" {
				result := parseMultipart(strings.NewReader(string(data)), nested)
				if result != "" {
					return result
				}
			}
		} else if strings.Contains(lower, "text/html") {
			htmlPart = string(data)
		} else if strings.Contains(lower, "text/plain") {
			textPart = string(data)
		}
	}

	if htmlPart != "" {
		return htmlPart
	}
	return textPart
}

func (m *Monitor) CheckNewEmails() ([]EmailResult, error) {
	if err := m.ensureConnection(); err != nil {
		return nil, fmt.Errorf("ensure connection: %w", err)
	}

	var results []EmailResult

	for _, sender := range m.cfg.SenderEmails {
		criteria := imap.NewSearchCriteria()
		criteria.Header.Set("From", sender)
		criteria.WithoutFlags = []string{imap.SeenFlag}

		uids, err := m.imapClient.UidSearch(criteria)
		if err != nil {
			slog.Error("Search failed", "sender", sender, "error", err)
			m.reconnect()
			return results, fmt.Errorf("search: %w", err)
		}

		if len(uids) == 0 {
			continue
		}

		slog.Info("Found new emails", "sender", sender, "count", len(uids))

		if len(uids) > m.cfg.MaxEmailsToCheck {
			uids = uids[len(uids)-m.cfg.MaxEmailsToCheck:]
		}

		seqSet := new(imap.SeqSet)
		seqSet.AddNum(uids...)

		section := &imap.BodySectionName{Peek: true}
		items := []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope, imap.FetchUid}

		messages := make(chan *imap.Message, len(uids))
		slog.Info("Fetching email bodies...", "count", len(uids))

		fetchDone := make(chan error, 1)
		go func() {
			err := m.imapClient.UidFetch(seqSet, items, messages)
			close(messages)
			fetchDone <- err
		}()

		select {
		case err := <-fetchDone:
			if err != nil {
				slog.Error("Fetch failed", "error", err)
				continue
			}
		case <-time.After(60 * time.Second):
			slog.Error("Fetch timed out, reconnecting...")
			m.reconnect()
			continue
		}
		slog.Info("Fetch complete, processing messages...")

		for msg := range messages {
			uid := msg.Uid

			if _, seen := m.processedUIDs[uid]; seen {
				continue
			}

			subject := ""
			if msg.Envelope != nil {
				subject = msg.Envelope.Subject
			}

			subject = strings.TrimPrefix(subject, "Fwd: ")
			subject = strings.TrimPrefix(subject, "Fwd ")

			slog.Info("Processing email", "uid", uid, "subject", subject)

			var body string
			for _, value := range msg.Body {
				b, err := io.ReadAll(value)
				if err != nil {
					slog.Error("Failed to read message body", "uid", uid, "error", err)
					continue
				}
				body = string(b)
			}

			parsed, err := mail.ReadMessage(strings.NewReader(body))
			if err == nil {
				body = getEmailBody(parsed)
			}

			link := m.extractLink(body)
			if link != "" {
				results = append(results, EmailResult{
					UID:     uid,
					Subject: subject,
					Link:    link,
				})
				m.processedUIDs[uid] = struct{}{}
				slog.Info("Email queued for processing", "uid", uid)
			} else {
				slog.Warn("No valid link found in email", "uid", uid)
				m.processedUIDs[uid] = struct{}{}
			}
		}
	}

	if len(results) == 0 {
		slog.Debug("No new emails found")
	}

	return results, nil
}
