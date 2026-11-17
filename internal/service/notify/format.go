package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

const (
	logLines        = 20
	logMaxAge       = 24 * time.Hour
	slackHeaderMax  = 150
	slackSectionMax = 3000
	slackLogMax     = 2800
	discordTitleMax = 256
	discordDescMax  = 4000
	discordLogMax   = 1000
)

// WorkloadURL is the UI link of a workload ("" without a public URL).
func WorkloadURL(publicURL, workloadID string) string {
	if publicURL == "" || workloadID == "" {
		return ""
	}
	esc := strings.ReplaceAll(url.QueryEscape(workloadID), "+", "%20")
	return strings.TrimRight(publicURL, "/") + "/workloads/" + esc
}

// logTypes are the issue types a crash-log snapshot can explain.
func logRelevant(t model.IssueType) bool {
	switch t {
	case model.IssueCrashLoop, model.IssueOOM, model.IssueDegraded, model.IssueJobFailed:
		return true
	}
	return false
}

// crashLogTail returns the last lines of the workload's newest crash log
// captured within the last day ("" when none / not applicable).
func (s *Service) crashLogTail(ctx context.Context, ch *model.NotificationChannel, ev model.IssueEvent) string {
	if ev.Kind == model.EventTest {
		if ch.IncludeLogs {
			return "starting server on :8080\nconnecting to database…\npanic: connection refused (sample log line)"
		}
		return ""
	}
	if !ch.IncludeLogs || ev.Kind == model.EventResolved || s.crashLogs == nil || !logRelevant(ev.Issue.Type) {
		return ""
	}
	logs, err := s.crashLogs.FindByWorkload(ctx, ev.Issue.WorkloadID, 1)
	if err != nil || len(logs) == 0 {
		return ""
	}
	l := logs[0]
	if s.opt.Now().Sub(l.OccurredAt) > logMaxAge {
		return ""
	}
	return tailLines(l.Lines, logLines)
}

func tailLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// tailBytes keeps the end of s within max runes, marking the cut.
func tailBytes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return "…" + string(r[len(r)-max+1:])
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func emoji(ev model.IssueEvent) string {
	switch {
	case ev.Kind == model.EventResolved:
		return "✅"
	case ev.Kind == model.EventTest:
		return "🔔"
	case ev.Issue.Severity == model.SeverityCritical:
		return "🔴"
	}
	return "🟠"
}

// headline is the one-line message title.
func headline(ev model.IssueEvent) string {
	t := ev.Issue.Title
	switch ev.Kind {
	case model.EventResolved:
		return "✅ Resolved: " + t
	case model.EventReminder:
		return emoji(ev) + " Still open: " + t
	}
	return emoji(ev) + " " + t
}

func subject(i model.Issue) string {
	parts := []string{i.Kind + "/" + i.Name}
	if i.Namespace != "" {
		parts = append(parts, i.Namespace)
	}
	if i.SourceName != "" {
		parts = append(parts, i.SourceName)
	}
	return strings.Join(parts, " · ")
}

func detailText(ev model.IssueEvent) string {
	d := ev.Issue.Detail
	if ev.Kind == model.EventResolved && ev.Issue.ResolvedAt != nil {
		open := ev.Issue.ResolvedAt.Sub(ev.Issue.FirstSeen).Round(time.Minute)
		if open > 0 {
			if d != "" {
				d += "\n"
			}
			d += "Was open for " + open.String()
		}
	}
	return d
}

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func codeBlock(s string) string {
	return "```\n" + strings.ReplaceAll(s, "```", "'''") + "\n```"
}

// build returns the request body and extra headers for the channel type.
func (s *Service) build(ctx context.Context, ch *model.NotificationChannel, ev model.IssueEvent, sec string) ([]byte, map[string]string, error) {
	link := WorkloadURL(s.opt.PublicURL, ev.Issue.WorkloadID)
	tail := s.crashLogTail(ctx, ch, ev)
	switch ch.Type {
	case model.ChannelSlack:
		b, err := json.Marshal(slackPayload(ev, link, tail))
		return b, nil, err
	case model.ChannelDiscord:
		b, err := json.Marshal(discordPayload(ev, link, tail, s.opt.Now()))
		return b, nil, err
	case model.ChannelWebhook:
		b, err := json.Marshal(webhookBody{
			Event: string(ev.Kind), Issue: ev.Issue, WorkloadURL: link, CrashLogTail: tail, SentAt: s.opt.Now(),
		})
		if err != nil {
			return nil, nil, err
		}
		h := map[string]string{"X-KYMO-Event": string(ev.Kind)}
		if sec != "" {
			h["X-KYMO-Signature"] = Sign(sec, b)
		}
		return b, h, nil
	}
	return nil, nil, fmt.Errorf("unknown channel type %q", ch.Type)
}

type webhookBody struct {
	Event        string      `json:"event"`
	Issue        model.Issue `json:"issue"`
	WorkloadURL  string      `json:"workload_url"`
	CrashLogTail string      `json:"crash_log_tail"`
	SentAt       time.Time   `json:"sent_at"`
}

func slackPayload(ev model.IssueEvent, link, tail string) map[string]any {
	i := ev.Issue
	head := headline(ev)
	blocks := []any{
		map[string]any{"type": "header", "text": map[string]any{"type": "plain_text", "text": clip(head, slackHeaderMax), "emoji": true}},
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn",
			"text": clip(fmt.Sprintf("*%s/%s*", slackEscape(i.Kind), slackEscape(i.Name))+slackSubjectRest(i), slackSectionMax)}},
	}
	if d := detailText(ev); d != "" {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": clip(slackEscape(d), slackSectionMax)}})
	}
	if tail != "" {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn",
			"text": codeBlock(tailBytes(slackEscape(tail), slackLogMax))}})
	}
	if link != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []any{map[string]any{
			"type": "button", "action_id": "open_kymo", "url": link,
			"text": map[string]any{"type": "plain_text", "text": "Open in KYMO"},
		}}})
	}
	return map[string]any{"text": head + " — " + subject(i), "blocks": blocks}
}

func slackSubjectRest(i model.Issue) string {
	var b strings.Builder
	if i.Namespace != "" {
		b.WriteString(" · " + slackEscape(i.Namespace))
	}
	if i.SourceName != "" {
		b.WriteString(" · " + slackEscape(i.SourceName))
	}
	return b.String()
}

func discordColor(ev model.IssueEvent) int {
	switch {
	case ev.Kind == model.EventResolved:
		return 0x30A46C
	case ev.Kind == model.EventTest:
		return 0x5E6AD2
	case ev.Issue.Severity == model.SeverityCritical:
		return 0xE5484D
	}
	return 0xF5A524
}

func discordPayload(ev model.IssueEvent, link, tail string, now time.Time) map[string]any {
	i := ev.Issue
	field := func(name, value string, inline bool) map[string]any {
		return map[string]any{"name": name, "value": clip(value, 1000), "inline": inline}
	}
	fields := []any{
		field("Workload", i.Kind+"/"+i.Name, true),
		field("Namespace", orDash(i.Namespace), true),
		field("Cluster", orDash(i.SourceName), true),
		field("Severity", string(i.Severity), true),
	}
	if tail != "" {
		// "```\n" + body + "\n```" must stay within the logs limit.
		body := tailBytes(strings.ReplaceAll(tail, "```", "'''"), discordLogMax-8)
		fields = append(fields, map[string]any{"name": "Last crash log", "value": "```\n" + body + "\n```", "inline": false})
	}
	desc := detailText(ev)
	if link != "" {
		if desc != "" {
			desc += "\n\n"
		}
		desc += "[Open in KYMO](" + link + ")"
	}
	embed := map[string]any{
		"title":     clip(headline(ev), discordTitleMax),
		"color":     discordColor(ev),
		"fields":    fields,
		"timestamp": now.UTC().Format(time.RFC3339),
		"footer":    map[string]any{"text": "KYMO"},
	}
	if desc != "" {
		embed["description"] = clip(desc, discordDescMax)
	}
	if link != "" {
		embed["url"] = link
	}
	return map[string]any{"username": "KYMO", "embeds": []any{embed}}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
