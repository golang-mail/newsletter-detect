// newsletter-detect classifies emails as newsletters / bulk / mailing-list mail.
//
// Returns matched=true when the score crosses `threshold`, optionally with a
// move action (configurable). Filter authors can use the plugin purely as a
// matcher (no action) and chain a separate move/flag action in the same filter,
// or let the plugin attach the move itself via `actions_on_match`.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"time"
)

type envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type initRequest struct {
	HostVersion string          `json:"host_version"`
	Config      json.RawMessage `json:"config,omitempty"`
}

type pluginAction struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

type pluginConfig struct {
	// Threshold: emails scoring >= this are considered newsletters.
	// Default 3. Each header signal is +1..+3; body footer signals +1.
	Threshold int `yaml:"threshold" json:"threshold"`
	// ActionsOnMatch are appended to the verdict when matched. Lets the plugin
	// itself drive a `move` so filters don't need a separate `actions:` block.
	ActionsOnMatch []pluginAction `yaml:"actions_on_match" json:"actions_on_match"`
	// Verbose logs the score for every evaluated email — handy for tuning.
	Verbose bool `yaml:"verbose" json:"verbose"`
	// MinScoreForBody: only consult body signals if header score is at least this.
	// Saves work on the obvious-yes and obvious-no cases.
	MinScoreForBody int `yaml:"min_score_for_body" json:"min_score_for_body"`
}

type address struct {
	PersonalName string `json:"PersonalName"`
	MailboxName  string `json:"MailboxName"`
	HostName     string `json:"HostName"`
}

type emailEnvelope struct {
	Date    time.Time  `json:"Date"`
	Subject string     `json:"Subject"`
	From    []*address `json:"From"`
	To      []*address `json:"To"`
	Cc      []*address `json:"Cc"`
}

type evalReq struct {
	Email struct {
		UID         uint32            `json:"uid"`
		Account     string            `json:"account"`
		Mailbox     string            `json:"mailbox"`
		Envelope    *emailEnvelope    `json:"envelope"`
		Headers     map[string]string `json:"headers"`
		Body        string            `json:"body"`
		ContentType string            `json:"content_type"`
	} `json:"email"`
	FilterConfig string `json:"filter_config"`
}

var cfg pluginConfig

func main() {
	log.SetFlags(0)
	path := os.Getenv("GO_MAIL_PLUGIN_SOCKET")
	if path == "" {
		log.Fatal("GO_MAIL_PLUGIN_SOCKET not set")
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		log.Fatalf("listen %s: %v", path, err)
	}
	_ = os.Chmod(path, 0o600)
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serve(conn)
	}
}

func serve(conn net.Conn) {
	defer conn.Close()
	in := bufio.NewScanner(conn)
	in.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	out := json.NewEncoder(conn)

	for in.Scan() {
		var msg envelope
		if err := json.Unmarshal(in.Bytes(), &msg); err != nil {
			log.Printf("bad envelope: %v", err)
			continue
		}
		switch msg.Method {
		case "init":
			handleInit(out, msg)
		case "evaluate":
			handleEvaluate(out, msg)
		case "shutdown":
			return
		}
	}
}

func handleInit(out *json.Encoder, msg envelope) {
	cfg.Threshold = 3
	cfg.MinScoreForBody = 1
	var req initRequest
	_ = json.Unmarshal(msg.Payload, &req)
	if len(req.Config) > 0 {
		if err := json.Unmarshal(req.Config, &cfg); err != nil {
			log.Printf("decode plugin config: %v", err)
		}
	}
	manifest, _ := json.Marshal(map[string]any{
		"name":      "newsletter-detect",
		"version":   "0.1.0",
		"actions":   []string{"move", "flag", "mark_as_read"},
		"wants":     []string{"envelope", "headers", "body"},
		"cacheable": true,
	})
	_ = out.Encode(envelope{
		ID:      msg.ID,
		Type:    "response",
		Method:  "init",
		Payload: manifest,
	})
	log.Printf("initialized: threshold=%d actions_on_match=%d", cfg.Threshold, len(cfg.ActionsOnMatch))
}

func handleEvaluate(out *json.Encoder, msg envelope) {
	var r evalReq
	if err := json.Unmarshal(msg.Payload, &r); err != nil {
		respond(out, msg.ID, map[string]any{"error": "decode evaluate: " + err.Error()})
		return
	}

	score, reasons := scoreNewsletter(&r)
	matched := score >= cfg.Threshold
	if cfg.Verbose {
		log.Printf("uid=%d acct=%s/%s subject=%q score=%d matched=%v reasons=%s",
			r.Email.UID, r.Email.Account, r.Email.Mailbox,
			truncate(envelopeSubject(r.Email.Envelope), 60), score, matched, strings.Join(reasons, ","))
	}

	resp := map[string]any{
		"matched": matched,
		"reason":  fmt.Sprintf("score=%d %s", score, strings.Join(reasons, ",")),
	}
	if matched && len(cfg.ActionsOnMatch) > 0 {
		resp["actions"] = cfg.ActionsOnMatch
	}
	respond(out, msg.ID, resp)
}

// scoreNewsletter assigns points based on header + body signals.
// The thresholds are tuned conservatively — the goal is "would a human call
// this a newsletter?" not a comprehensive bulk-mail filter.
func scoreNewsletter(r *evalReq) (int, []string) {
	score := 0
	var reasons []string
	add := func(n int, reason string) {
		score += n
		reasons = append(reasons, fmt.Sprintf("%s+%d", reason, n))
	}

	h := r.Email.Headers
	get := func(name string) string {
		if h == nil {
			return ""
		}
		for k, v := range h {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return ""
	}

	if v := get("List-Unsubscribe"); v != "" {
		add(3, "list-unsubscribe")
	}
	if v := get("List-Id"); v != "" {
		add(2, "list-id")
	}
	if v := get("List-Post"); v != "" {
		add(1, "list-post")
	}
	if v := get("Mailing-List"); v != "" {
		add(1, "mailing-list")
	}
	if v := strings.ToLower(get("Precedence")); v == "bulk" || v == "list" {
		add(2, "precedence-"+v)
	}
	if v := strings.ToLower(get("Auto-Submitted")); v != "" && v != "no" {
		add(1, "auto-submitted")
	}
	if v := get("X-Mailer"); v != "" {
		l := strings.ToLower(v)
		// Catch the obvious bulk senders. Not exhaustive — just confirmation signal.
		for _, m := range []string{"mailchimp", "sendgrid", "marketo", "campaign", "mailgun", "constantcontact", "klaviyo", "postmark"} {
			if strings.Contains(l, m) {
				add(2, "x-mailer-"+m)
				break
			}
		}
	}
	if v := strings.ToLower(get("X-Campaign")); v != "" {
		add(1, "x-campaign")
	}

	// Only inspect body if at least one header signal already fired (saves work).
	if score >= cfg.MinScoreForBody && r.Email.Body != "" {
		if bs := scoreBody(r.Email.Body, r.Email.ContentType); bs > 0 {
			add(bs, "body-signals")
		}
	}

	return score, reasons
}

var (
	unsubscribeRE = regexp.MustCompile(`(?i)\b(unsubscribe|opt[- ]?out|email preferences|manage preferences)\b`)
	viewBrowserRE = regexp.MustCompile(`(?i)\b(view (this|in) (your )?browser|having trouble (viewing|reading)|see online version)\b`)
	footerLinkRE  = regexp.MustCompile(`(?i)<a[^>]*href=["'][^"']*(unsubscribe|preferences|optout)[^"']*["']`)
	trackingPixRE = regexp.MustCompile(`(?i)<img[^>]*\b(width\s*=\s*["']?1|height\s*=\s*["']?1|spacer|pixel|tracking)\b[^>]*>`)
)

func scoreBody(body, ct string) int {
	score := 0
	low := strings.ToLower(body)
	isHTML := strings.Contains(strings.ToLower(ct), "html") ||
		(ct == "" && strings.Contains(low, "<html"))

	// Plain-text "unsubscribe" word: weak — many newsletters mention it but so do legit "you can unsubscribe at any time" footers in account emails.
	if unsubscribeRE.MatchString(low) {
		score++
	}
	if viewBrowserRE.MatchString(low) {
		score++
	}
	if isHTML {
		if footerLinkRE.MatchString(body) {
			score++
		}
		if trackingPixRE.MatchString(body) {
			score++
		}
	}
	return score
}

func envelopeSubject(e *emailEnvelope) string {
	if e == nil {
		return ""
	}
	return e.Subject
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func respond(out *json.Encoder, id string, payload any) {
	raw, _ := json.Marshal(payload)
	_ = out.Encode(envelope{
		ID:      id,
		Type:    "response",
		Method:  "evaluate",
		Payload: raw,
	})
}
