package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	sendTimeout  = 10 * time.Second
	maxURLLength = 1024
)

// ErrBlockedAddress is returned when a webhook resolves to a private,
// loopback or otherwise internal address.
var ErrBlockedAddress = errors.New("webhook address is not allowed (private, loopback or internal network)")

// Sender posts notifications. Webhook URLs are user-supplied and fetched
// by the server, so it guards against SSRF: https only, and the dialer
// refuses internal IPs at connect time (after DNS resolution, so a
// hostname that resolves to 127.0.0.1 or 169.254.169.254 is refused too).
// AllowPrivate lifts both rules for local development only.
type Sender struct {
	client       *http.Client
	allowPrivate bool
}

func NewSender(allowPrivate bool) *Sender {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || blockedIP(ip) {
				return ErrBlockedAddress
			}
			return nil
		}
	}
	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		Proxy:               nil, // a proxy would bypass the address check
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConns:        10,
	}
	return &Sender{
		allowPrivate: allowPrivate,
		client: &http.Client{
			Timeout:   sendTimeout,
			Transport: transport,
			// Redirects could point at an internal address on a path the
			// dialer still checks, but there is no reason to follow them.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// blockedIP covers loopback, private (RFC 1918, ULA), link-local (incl.
// cloud metadata 169.254.169.254), CGNAT, multicast and unspecified.
func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	return netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("192.0.0.0/24").Contains(ip)
}

// ValidateURL checks a webhook URL before it is stored.
func (s *Sender) ValidateURL(raw string) error {
	if len(raw) > maxURLLength {
		return fmt.Errorf("webhook URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("webhook URL must be an absolute URL")
	}
	if u.Scheme != "https" && !(s.allowPrivate && u.Scheme == "http") {
		return fmt.Errorf("webhook URL must use https")
	}
	if u.User != nil {
		return fmt.Errorf("webhook URL must not contain credentials")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !s.allowPrivate && blockedIP(ip) {
		return ErrBlockedAddress
	}
	return nil
}

// Notice is one message: problems that started and ended since the last
// check, or a test.
type Notice struct {
	Cluster  string
	New      []Cause
	Resolved []Cause
	Test     bool
	Link     string // where to look, e.g. the cluster's health scan page
}

// Send posts n to the configured webhook in its kind's format.
func (s *Sender) Send(ctx context.Context, kind, rawURL string, n Notice) error {
	if err := s.ValidateURL(rawURL); err != nil {
		return err
	}
	body, err := Format(kind, n)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "KubeAssist-Alerts/1")
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlockedAddress) {
			return ErrBlockedAddress
		}
		return fmt.Errorf("delivering alert: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("webhook returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

const maxListed = 5

// Format renders a notice as the webhook's JSON payload.
func Format(kind string, n Notice) ([]byte, error) {
	switch kind {
	case KindSlack:
		return json.Marshal(map[string]string{"text": text(n, "*", "`", func(label, u string) string { return "<" + u + "|" + label + ">" })})
	case KindDiscord:
		msg := text(n, "**", "`", func(label, u string) string { return "[" + label + "](" + u + ")" })
		if len(msg) > 1900 {
			msg = msg[:1900] + "…"
		}
		return json.Marshal(map[string]string{"content": msg})
	case KindWebhook:
		event := "change"
		if n.Test {
			event = "test"
		}
		return json.Marshal(map[string]any{
			"source": "kubeassist", "event": event, "cluster": n.Cluster,
			"new": nonNil(n.New), "resolved": nonNil(n.Resolved), "url": n.Link,
		})
	}
	return nil, fmt.Errorf("unknown webhook kind %q", kind)
}

func nonNil(c []Cause) []Cause {
	if c == nil {
		return []Cause{}
	}
	return c
}

// text renders the human message with the target's markup.
func text(n Notice, bold, code string, link func(label, url string) string) string {
	var b strings.Builder
	if n.Test {
		fmt.Fprintf(&b, "✅ %sKubeAssist test alert%s for cluster %s%s%s — this webhook works. You'll be notified here when problems start or are resolved.", bold, bold, code, n.Cluster, code)
		return b.String()
	}
	if len(n.New) > 0 {
		fmt.Fprintf(&b, "🔴 %s%d new problem%s in %s%s%s%s\n", bold, len(n.New), plural(len(n.New)), code, n.Cluster, code, bold)
		for i, c := range n.New {
			if i == maxListed {
				fmt.Fprintf(&b, "…and %d more\n", len(n.New)-maxListed)
				break
			}
			fmt.Fprintf(&b, "• %s%s%s (%s) — %s\n", bold, c.Title, bold, c.FailureType, podList(c.Pods, code))
			if c.Fix != "" {
				fmt.Fprintf(&b, "   Fix: %s%s%s\n", code, c.Fix, code)
			}
		}
	}
	if len(n.Resolved) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "✅ %sResolved in %s%s%s%s\n", bold, code, n.Cluster, code, bold)
		for i, c := range n.Resolved {
			if i == maxListed {
				fmt.Fprintf(&b, "…and %d more\n", len(n.Resolved)-maxListed)
				break
			}
			fmt.Fprintf(&b, "• %s\n", c.Title)
		}
	}
	if n.Link != "" {
		b.WriteString(link("Open the health scan", n.Link))
	}
	return strings.TrimRight(b.String(), "\n")
}

func podList(pods []string, code string) string {
	shown := pods
	if len(shown) > 3 {
		shown = shown[:3]
	}
	parts := make([]string, len(shown))
	for i, p := range shown {
		parts[i] = code + p + code
	}
	s := strings.Join(parts, ", ")
	if len(pods) > 3 {
		s += fmt.Sprintf(" and %d more", len(pods)-3)
	}
	return fmt.Sprintf("%d pod%s: %s", len(pods), plural(len(pods)), s)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// MaskURL shows enough of a webhook URL to recognize it, never the
// secret path: "https://hooks.slack.com/…/AbCd".
func MaskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "…"
	}
	tail := strings.TrimRight(u.Path, "/")
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return u.Scheme + "://" + u.Host + "/…" + tail
}
