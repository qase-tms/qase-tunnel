package translate

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTranslate_Wsarecv(t *testing.T) {
	cases := []string{
		`http: proxy error: read tcp ...: wsarecv: An existing connection was forcibly closed by the remote host`,
		`http: proxy error: read tcp ...: wsasend: ...`,
		`Foi forçado o cancelamento de uma conexão pelo software no computador host`,
	}
	for _, line := range cases {
		t.Run(line[:min(40, len(line))], func(t *testing.T) {
			tr := Translate(line)
			assert.True(t, tr.Translated())
			assert.Equal(t, "wsarecv", tr.Pattern.Anchor)
			assert.Contains(t, tr.AnchorURL(), "#wsarecv")
		})
	}
}

func TestTranslate_BearfoosQuarantine(t *testing.T) {
	cases := []string{
		`Microsoft Defender quarantined Trojan:Win32/Bearfoos.A!ml from frpc.exe`,
		`defender quarantined frpc.exe`,
		`Trojan:Win32/Bearfoos detected`,
	}
	for _, line := range cases {
		tr := Translate(line)
		assert.True(t, tr.Translated(), line)
		assert.Equal(t, "bearfoos-quarantine", tr.Pattern.Anchor)
	}
}

func TestTranslate_X509UnknownAuthority(t *testing.T) {
	cases := []string{
		`x509: certificate signed by unknown authority`,
		`tls: failed to verify certificate: x509: certificate signed by unknown authority`,
	}
	for _, line := range cases {
		tr := Translate(line)
		assert.True(t, tr.Translated(), line)
		assert.Equal(t, "x509-unknown-authority", tr.Pattern.Anchor)
	}
}

func TestTranslate_DialTCPTimeout(t *testing.T) {
	cases := []string{
		`dial tcp 1.2.3.4:7000: i/o timeout`,
		`dialTCP: lookup frps.qase.io: i/o timeout`,
		`dial tcp: i/o timeout`,
	}
	for _, line := range cases {
		tr := Translate(line)
		assert.True(t, tr.Translated(), line)
		assert.Equal(t, "dial-tcp-timeout", tr.Pattern.Anchor)
	}
}

func TestTranslate_ConnectionRefused(t *testing.T) {
	cases := []string{
		`dial tcp 1.2.3.4:7000: connect: connection refused`,
		`connectex: ... actively refused`,
		`No connection could be made because the target machine actively refused it`,
	}
	for _, line := range cases {
		tr := Translate(line)
		assert.True(t, tr.Translated(), line)
		assert.Equal(t, "connection-refused", tr.Pattern.Anchor)
	}
}

func TestTranslate_NoSuchHost(t *testing.T) {
	cases := []string{
		`dial tcp: lookup frps.qase.io: no such host`,
		`getaddrinfow: No such host is known`,
	}
	for _, line := range cases {
		tr := Translate(line)
		assert.True(t, tr.Translated(), line)
		assert.Equal(t, "no-such-host", tr.Pattern.Anchor)
	}
}

func TestTranslate_LoginFailed(t *testing.T) {
	cases := []string{
		`[E] [service.go:144] login to server failed: authentication failed`,
		`invalid authentication: token expired`,
		`invalid user token`,
	}
	for _, line := range cases {
		tr := Translate(line)
		assert.True(t, tr.Translated(), line)
		assert.Equal(t, "login-failed", tr.Pattern.Anchor)
	}
}

func TestTranslate_StcpSecretMismatch(t *testing.T) {
	cases := []string{
		`stcp visitor secret invalid`,
		`xtcp secret mismatch detected`,
	}
	for _, line := range cases {
		tr := Translate(line)
		assert.True(t, tr.Translated(), line)
		assert.Equal(t, "stcp-secret-mismatch", tr.Pattern.Anchor)
	}
}

func TestTranslate_TunnelRevoked(t *testing.T) {
	cases := []string{
		`tunnel was revoked by the server`,
		`tunnel is revoked`,
	}
	for _, line := range cases {
		tr := Translate(line)
		assert.True(t, tr.Translated(), line)
		assert.Equal(t, "tunnel-revoked", tr.Pattern.Anchor)
	}
}

func TestTranslate_UnknownLineNotTranslated(t *testing.T) {
	tr := Translate("just a regular informational log line")
	assert.False(t, tr.Translated())
	assert.Equal(t, "", tr.AnchorURL())
}

func TestTranslate_EmptyLineNotTranslated(t *testing.T) {
	tr := Translate("")
	assert.False(t, tr.Translated())
}

func TestTranslate_PriorityFirstMatchWins(t *testing.T) {
	// stcp-secret-mismatch precedes login-failed; a line containing both
	// should match stcp-secret-mismatch.
	line := `login to server failed because xtcp secret mismatch`
	tr := Translate(line)
	assert.True(t, tr.Translated())
	assert.Equal(t, "stcp-secret-mismatch", tr.Pattern.Anchor)
}

func TestTranslate_TranslateWithCustomPatternsRespectsOrder(t *testing.T) {
	patterns := []Pattern{
		{Anchor: "first", Match: regexp.MustCompile(`(?i)foo`), Headline: "First", Banner: "First match"},
		{Anchor: "second", Match: regexp.MustCompile(`(?i)foo`), Headline: "Second", Banner: "Second match"},
	}
	tr := TranslateWith(patterns, "FOOBAR")
	assert.Equal(t, "first", tr.Pattern.Anchor)
}

func TestTranslate_FormatBannerIncludesAnchorAndOriginal(t *testing.T) {
	tr := Translate(`x509: certificate signed by unknown authority`)
	out := tr.FormatBanner()
	assert.True(t, strings.Contains(out, "#x509-unknown-authority"))
	assert.True(t, strings.Contains(out, "Original:"))
	assert.True(t, strings.Contains(out, "x509:"))
}

func TestTranslate_FormatBannerForUnmatchedReturnsOriginalOnly(t *testing.T) {
	tr := Translate("plain line")
	out := tr.FormatBanner()
	assert.Equal(t, "plain line", out)
}

func TestTranslate_AllDefaultPatternsHaveAnchorsHeadlinesBanners(t *testing.T) {
	for _, p := range DefaultPatterns {
		assert.NotEmpty(t, p.Anchor, "pattern missing anchor")
		assert.NotEmpty(t, p.Headline, "pattern %q missing headline", p.Anchor)
		assert.NotEmpty(t, p.Banner, "pattern %q missing banner", p.Anchor)
		assert.NotNil(t, p.Match, "pattern %q missing regex", p.Anchor)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
