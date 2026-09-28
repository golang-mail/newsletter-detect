package main

import (
	"strings"
	"testing"
)

// helper to build an evalReq with given headers + body.
func reqWith(headers map[string]string, body, ct string) *evalReq {
	r := &evalReq{}
	r.Email.Headers = headers
	r.Email.Body = body
	r.Email.ContentType = ct
	return r
}

func TestScoreNewsletter_HeaderSignals(t *testing.T) {
	cases := []struct {
		name       string
		headers    map[string]string
		minScore   int
		mustReason string
	}{
		{
			name:       "list-unsubscribe alone scores 3",
			headers:    map[string]string{"List-Unsubscribe": "<mailto:u@example.com>"},
			minScore:   3,
			mustReason: "list-unsubscribe",
		},
		{
			name:       "list-id alone scores 2",
			headers:    map[string]string{"List-Id": "<news.example.com>"},
			minScore:   2,
			mustReason: "list-id",
		},
		{
			name:       "precedence bulk scores 2",
			headers:    map[string]string{"Precedence": "bulk"},
			minScore:   2,
			mustReason: "precedence-bulk",
		},
		{
			name:       "x-mailer mailchimp scores 2",
			headers:    map[string]string{"X-Mailer": "MailChimp Mailer - **CID01**"},
			minScore:   2,
			mustReason: "x-mailer-mailchimp",
		},
		{
			name: "all-the-things scores high",
			headers: map[string]string{
				"List-Unsubscribe": "<mailto:u@example.com>",
				"List-Id":          "<news.example.com>",
				"Precedence":       "bulk",
				"X-Mailer":         "Sendgrid",
			},
			minScore:   8,
			mustReason: "list-unsubscribe",
		},
		{
			name:     "empty headers score 0",
			headers:  map[string]string{},
			minScore: 0,
		},
		{
			name:     "irrelevant headers score 0",
			headers:  map[string]string{"X-Coffee": "yes please"},
			minScore: 0,
		},
	}

	// Reset config so MinScoreForBody doesn't interfere.
	cfg = pluginConfig{Threshold: 3, MinScoreForBody: 99}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score, reasons := scoreNewsletter(reqWith(tc.headers, "", ""))
			if score < tc.minScore {
				t.Errorf("score=%d, want >= %d (reasons=%v)", score, tc.minScore, reasons)
			}
			if tc.mustReason != "" {
				found := false
				for _, r := range reasons {
					if strings.Contains(r, tc.mustReason) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("reasons=%v missing %q", reasons, tc.mustReason)
				}
			}
		})
	}
}

func TestScoreNewsletter_HeaderCaseInsensitive(t *testing.T) {
	// Real email headers come in mixed case; the plugin must match them regardless.
	cfg = pluginConfig{Threshold: 3, MinScoreForBody: 99}
	score, _ := scoreNewsletter(reqWith(map[string]string{
		"list-UNSUBSCRIBE": "<mailto:u@example.com>",
	}, "", ""))
	if score < 3 {
		t.Errorf("score=%d, want >= 3 (header lookup is supposed to be case-insensitive)", score)
	}
}

func TestScoreNewsletter_BodySignalsGatedByMinScore(t *testing.T) {
	htmlBody := `
<html>
  Hi! <a href="https://news.example.com/unsubscribe?u=1">Unsubscribe</a>
  <a href="https://news.example.com/view">View this in your browser</a>
  <img src="track.gif" width="1" height="1">
</html>`

	// Without any header signals, MinScoreForBody=1 prevents body inspection.
	cfg = pluginConfig{Threshold: 3, MinScoreForBody: 1}
	score, _ := scoreNewsletter(reqWith(map[string]string{}, htmlBody, "text/html"))
	if score != 0 {
		t.Errorf("score=%d, want 0 (body should be ignored when no header signals fire)", score)
	}

	// With one header signal, body inspection kicks in and adds points.
	score2, reasons := scoreNewsletter(reqWith(map[string]string{"Precedence": "bulk"}, htmlBody, "text/html"))
	if score2 <= 2 {
		t.Errorf("score=%d, want > 2 (body should add to the precedence score). reasons=%v", score2, reasons)
	}
}

func TestScoreNewsletter_PlainTextBody(t *testing.T) {
	cfg = pluginConfig{Threshold: 3, MinScoreForBody: 0}
	body := "To unsubscribe, reply with STOP."
	score, _ := scoreNewsletter(reqWith(map[string]string{}, body, "text/plain"))
	// One signal: the unsubscribe regex match. No html-specific signals.
	if score != 1 {
		t.Errorf("score=%d, want exactly 1 (only unsubscribe regex)", score)
	}
}
