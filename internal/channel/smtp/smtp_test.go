package smtp_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	cryptotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	smtpch "github.com/ryan-evans-git/signalwatch/internal/channel/smtp"
)

// ---------- fake SMTP server ----------
//
// fakeSMTP speaks just enough SMTP to satisfy net/smtp's client: greet on
// connect, accept HELO/EHLO, MAIL, RCPT, DATA (collect lines until "."),
// and QUIT. Each session is single-threaded; tests serialize on session
// completion. The captured envelope (from, to, body) is exposed via
// methods so assertions are straightforward.
//
// The server is intentionally permissive: any AUTH challenge succeeds.
// Tests assert on what the client sent, not on server-side validation.

// failMode selects which protocol step the fake server rejects with a
// 5xx response. failNone is the happy path used by success-case tests.
type failMode int

const (
	failNone failMode = iota
	failGreet         // send 421 instead of 220; NewClient fails
	failEHLO          // 5xx on EHLO + HELO; client.Mail() falls through
	failMail          // 550 on MAIL FROM
	failRcpt          // 550 on RCPT TO
	failData          // 554 on DATA
)

type fakeSMTP struct {
	listener net.Listener
	useTLS   bool
	fail     failMode

	mu       sync.Mutex
	from     string
	to       []string
	dataBody []byte
	gotAuth  bool

	done chan struct{}
}

func newFakeSMTP(t *testing.T, useTLS bool) *fakeSMTP {
	return newFakeSMTPMode(t, useTLS, failNone)
}

func newFakeSMTPMode(t *testing.T, useTLS bool, fail failMode) *fakeSMTP {
	t.Helper()
	var (
		ln  net.Listener
		err error
	)
	if useTLS {
		cert := selfSignedCert(t)
		ln, err = cryptotls.Listen("tcp", "127.0.0.1:0", &cryptotls.Config{
			Certificates: []cryptotls.Certificate{cert},
			MinVersion:   cryptotls.VersionTLS12,
		})
	} else {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTP{listener: ln, useTLS: useTLS, fail: fail, done: make(chan struct{})}
	go s.accept()
	t.Cleanup(func() {
		_ = ln.Close()
	})
	return s
}

func (s *fakeSMTP) addr() string { return s.listener.Addr().String() }

func (s *fakeSMTP) accept() {
	defer close(s.done)
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	s.handle(conn)
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	write := func(line string) {
		_, _ = bw.WriteString(line + "\r\n")
		_ = bw.Flush()
	}

	if s.fail == failGreet {
		write("421 service down")
		return
	}
	write("220 fakeSMTP ready")

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "HELO") || strings.HasPrefix(upper, "EHLO"):
			if s.fail == failEHLO {
				write("502 command not implemented")
				continue
			}
			// Advertise AUTH so net/smtp will try PlainAuth when configured.
			write("250-fakeSMTP")
			write("250-AUTH PLAIN")
			write("250 OK")
		case strings.HasPrefix(upper, "AUTH"):
			s.mu.Lock()
			s.gotAuth = true
			s.mu.Unlock()
			write("235 Authentication succeeded")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			if s.fail == failMail {
				write("550 mailbox unavailable")
				continue
			}
			s.mu.Lock()
			s.from = strings.TrimPrefix(line[len("MAIL FROM:"):], " ")
			s.mu.Unlock()
			write("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			if s.fail == failRcpt {
				write("550 no such user")
				continue
			}
			s.mu.Lock()
			s.to = append(s.to, strings.TrimPrefix(line[len("RCPT TO:"):], " "))
			s.mu.Unlock()
			write("250 OK")
		case upper == "DATA":
			if s.fail == failData {
				write("554 transaction failed")
				continue
			}
			write("354 End data with <CR><LF>.<CR><LF>")
			var body []byte
			for {
				dline, err := br.ReadString('\n')
				if err != nil {
					return
				}
				trimmed := strings.TrimRight(dline, "\r\n")
				if trimmed == "." {
					break
				}
				body = append(body, dline...)
			}
			s.mu.Lock()
			s.dataBody = body
			s.mu.Unlock()
			write("250 OK")
		case upper == "QUIT":
			write("221 Bye")
			return
		case upper == "NOOP" || upper == "RSET":
			write("250 OK")
		default:
			write("250 OK")
		}
	}
}

func (s *fakeSMTP) snapshot() (from string, to []string, body string, gotAuth bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]string(nil), s.to...)
	return s.from, cp, string(s.dataBody), s.gotAuth
}

// selfSignedCert generates a fresh ECDSA cert for "127.0.0.1" valid for
// the next hour. Used to spin up a TLS-capable fake SMTP server.
func selfSignedCert(t *testing.T) cryptotls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"127.0.0.1", "localhost"},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return cryptotls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}
}

// ---------- tests ----------

func parseHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return host, port
}

func sampleNotification(address string) channel.Notification {
	return channel.Notification{
		IncidentID:  "inc-1",
		RuleID:      "r-1",
		RuleName:    "cpu high",
		Severity:    "warning",
		Description: "load > 90 for 5m",
		Value:       "95",
		Kind:        "firing",
		Address:     address,
		TriggeredAt: time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
	}
}

func TestNew_DefaultsName(t *testing.T) {
	c := smtpch.New(smtpch.Config{})
	if c.Name() != "smtp" {
		t.Fatalf("default Name: want smtp, got %q", c.Name())
	}
}

func TestNew_RespectsConfigName(t *testing.T) {
	c := smtpch.New(smtpch.Config{Name: "ops-mail"})
	if c.Name() != "ops-mail" {
		t.Fatalf("Name: want ops-mail, got %q", c.Name())
	}
}

func TestSend_EmptyAddressErrors(t *testing.T) {
	c := smtpch.New(smtpch.Config{Host: "127.0.0.1", Port: 25, From: "alert@example.com"})
	err := c.Send(context.Background(), sampleNotification(""))
	if err == nil || !strings.Contains(err.Error(), "empty address") {
		t.Fatalf("want empty-address error, got %v", err)
	}
}

func TestSend_PlainSMTPSuccess(t *testing.T) {
	srv := newFakeSMTP(t, false)
	host, port := parseHostPort(t, srv.addr())
	c := smtpch.New(smtpch.Config{
		Host:     host,
		Port:     port,
		Username: "user",
		Password: "pass",
		From:     "alert@example.com",
		UseTLS:   false,
	})
	if err := c.Send(context.Background(), sampleNotification("dest@example.com")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-srv.done
	from, to, body, gotAuth := srv.snapshot()
	if !strings.Contains(from, "alert@example.com") {
		t.Errorf("from: want alert@example.com, got %q", from)
	}
	if len(to) != 1 || !strings.Contains(to[0], "dest@example.com") {
		t.Errorf("to: want [dest@example.com], got %v", to)
	}
	if !strings.Contains(body, "Subject:") {
		t.Errorf("body should include Subject header, got %q", body)
	}
	if !strings.Contains(body, "[FIRING] WARNING: cpu high") {
		t.Errorf("subject body missing rule render, got %q", body)
	}
	if !gotAuth {
		t.Errorf("expected client to authenticate (Username was set)")
	}
}

func TestSend_PlainSMTPNoAuthWhenUsernameEmpty(t *testing.T) {
	srv := newFakeSMTP(t, false)
	host, port := parseHostPort(t, srv.addr())
	c := smtpch.New(smtpch.Config{
		Host: host, Port: port, From: "alert@example.com",
	})
	if err := c.Send(context.Background(), sampleNotification("dest@example.com")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-srv.done
	_, _, _, gotAuth := srv.snapshot()
	if gotAuth {
		t.Errorf("expected NO auth when Username is empty")
	}
}

func TestSend_TLSSuccess_WithInjectedTLSConfig(t *testing.T) {
	srv := newFakeSMTP(t, true)
	host, port := parseHostPort(t, srv.addr())
	// The default tls.Config in sendTLS would verify the self-signed
	// cert against the system roots and fail. Inject one that trusts
	// the self-signed test cert.
	c := smtpch.New(smtpch.Config{
		Host:     host,
		Port:     port,
		Username: "user",
		Password: "pass",
		From:     "alert@example.com",
		UseTLS:   true,
		TLSConfig: &cryptotls.Config{
			ServerName:         host,
			InsecureSkipVerify: true, //nolint:gosec // self-signed cert in test
			MinVersion:         cryptotls.VersionTLS12,
		},
	})
	if err := c.Send(context.Background(), sampleNotification("dest@example.com")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-srv.done
	from, to, body, _ := srv.snapshot()
	if !strings.Contains(from, "alert@example.com") || len(to) != 1 || !strings.Contains(body, "Subject:") {
		t.Errorf("TLS path didn't deliver: from=%q to=%v body[%d]=%q", from, to, len(body), body)
	}
}

// TLS dial against a port that isn't listening surfaces the err return
// from tls.Dial; covers the early-error branch in sendTLS.
func TestSend_TLSDialError(t *testing.T) {
	// Open and immediately close a listener to grab a free port that
	// nobody is listening on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	host, port := parseHostPort(t, ln.Addr().String())
	_ = ln.Close()

	c := smtpch.New(smtpch.Config{
		Host: host, Port: port, From: "alert@example.com", UseTLS: true,
		TLSConfig: &cryptotls.Config{
			ServerName:         host,
			InsecureSkipVerify: true, //nolint:gosec // synthetic test
			MinVersion:         cryptotls.VersionTLS12,
		},
	})
	err = c.Send(context.Background(), sampleNotification("dest@example.com"))
	if err == nil {
		t.Fatalf("want dial error")
	}
}

// TLS protocol-failure matrix: each failure mode covers one of the
// internal "if err != nil { return err }" branches in sendTLS, lifting
// the package above the 90% gate.
func TestSend_TLSProtocolErrors(t *testing.T) {
	insecure := func(host string) *cryptotls.Config {
		return &cryptotls.Config{
			ServerName:         host,
			InsecureSkipVerify: true, //nolint:gosec // self-signed test cert
			MinVersion:         cryptotls.VersionTLS12,
		}
	}
	cases := []struct {
		name     string
		mode     failMode
		username string // set to drive auth path
	}{
		{"NewClient fails on bad greeting", failGreet, ""},
		{"AUTH fails", failEHLO, "user"}, // EHLO fails -> Hello falls back -> Auth path returns err
		{"MAIL FROM rejected", failMail, ""},
		{"RCPT TO rejected", failRcpt, ""},
		{"DATA rejected", failData, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeSMTPMode(t, true, tc.mode)
			host, port := parseHostPort(t, srv.addr())
			c := smtpch.New(smtpch.Config{
				Host: host, Port: port, From: "alert@example.com",
				Username: tc.username, Password: "p",
				UseTLS: true, TLSConfig: insecure(host),
			})
			err := c.Send(context.Background(), sampleNotification("dest@example.com"))
			if err == nil {
				t.Fatalf("want error for failMode=%d, got nil", tc.mode)
			}
		})
	}
}

// sendTLS uses the default tls.Config when Config.TLSConfig is nil. Hitting
// a fake TLS server with a self-signed cert and no override should fail
// verification — exercising the nil-TLSConfig branch.
func TestSend_TLS_DefaultConfigRejectsSelfSigned(t *testing.T) {
	srv := newFakeSMTP(t, true)
	host, port := parseHostPort(t, srv.addr())
	c := smtpch.New(smtpch.Config{
		Host: host, Port: port, From: "alert@example.com", UseTLS: true,
		// no TLSConfig → sendTLS builds the strict default
	})
	err := c.Send(context.Background(), sampleNotification("dest@example.com"))
	if err == nil {
		t.Fatalf("want TLS verify error against self-signed server")
	}
	// Sanity: we shouldn't trip an unrelated error like io.EOF before
	// the handshake even begins.
	if errors.Is(err, io.EOF) {
		t.Fatalf("got EOF, expected x509/tls verify error: %v", err)
	}
}
