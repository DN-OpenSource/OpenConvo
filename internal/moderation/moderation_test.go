package moderation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openstream/openstream/internal/domain"
)

func TestBlocklistModes(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		words   []string
		text    string
		matches bool
	}{
		{"exact hit", "exact", []string{"badword"}, "this is a badword here", true},
		{"exact case-insensitive", "exact", []string{"badword"}, "BADWORD!", true},
		{"exact respects word boundary", "exact", []string{"ass"}, "assassin class", false},
		{"exact miss", "exact", []string{"badword"}, "totally fine", false},
		{"wildcard suffix", "wildcard", []string{"spam*"}, "spamming again", true},
		{"wildcard miss", "wildcard", []string{"spam*"}, "no problem", false},
		{"regex", "regex", []string{`(?i)f+r+e+e+ *m+o+n+e+y+`}, "FREE MONEY now", true},
		{"invalid regex skipped", "regex", []string{"(unclosed", "ok"}, "ok", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewBlocklistMatcher(&domain.Blocklist{Name: "t", Mode: tc.mode, Behavior: "flag", Words: tc.words})
			if got := m.Matches(tc.text); got != tc.matches {
				t.Fatalf("Matches(%q) = %v, want %v", tc.text, got, tc.matches)
			}
		})
	}
}

func TestBlocklistBehaviorVerdicts(t *testing.T) {
	for behavior, want := range map[string]Verdict{
		"block":        VerdictBlock,
		"flag":         VerdictFlag,
		"shadow_block": VerdictShadow,
	} {
		m := NewBlocklistMatcher(&domain.Blocklist{Name: "t", Mode: "exact", Behavior: behavior, Words: []string{"bad"}})
		d, err := m.Check(context.Background(), "app", &domain.Message{Text: "bad thing"})
		if err != nil || d.Verdict != want {
			t.Fatalf("behavior %s: verdict %v err %v", behavior, d.Verdict, err)
		}
	}
}

func TestPipelineFirstDecisionWins(t *testing.T) {
	flagList := NewBlocklistMatcher(&domain.Blocklist{Name: "a", Mode: "exact", Behavior: "flag", Words: []string{"flagme"}})
	blockList := NewBlocklistMatcher(&domain.Blocklist{Name: "b", Mode: "exact", Behavior: "block", Words: []string{"flagme"}})
	p := NewPipeline(flagList, blockList)
	d := p.Check(context.Background(), "app", &domain.Message{Text: "flagme"})
	if d.Verdict != VerdictFlag {
		t.Fatalf("first stage must win, got %v", d.Verdict)
	}
	d = p.Check(context.Background(), "app", &domain.Message{Text: "clean"})
	if d.Verdict != VerdictAllow {
		t.Fatalf("clean text must pass, got %v", d.Verdict)
	}
}

func TestWebhookClassifier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"verdict":"shadow","reason":"toxic"}`))
	}))
	defer srv.Close()

	c := &WebhookClassifier{URL: srv.URL}
	d, err := c.Check(context.Background(), "app", &domain.Message{Text: "x"})
	if err != nil || d.Verdict != VerdictShadow {
		t.Fatalf("verdict %v err %v", d.Verdict, err)
	}
}

func TestWebhookClassifierTimeoutFailOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	open := &WebhookClassifier{URL: srv.URL, Timeout: 50 * time.Millisecond}
	d, _ := open.Check(context.Background(), "app", &domain.Message{Text: "x"})
	if d.Verdict != VerdictAllow {
		t.Fatalf("fail-open must allow, got %v", d.Verdict)
	}

	closed := &WebhookClassifier{URL: srv.URL, Timeout: 50 * time.Millisecond, FailClosed: true}
	d, _ = closed.Check(context.Background(), "app", &domain.Message{Text: "x"})
	if d.Verdict != VerdictBlock {
		t.Fatalf("fail-closed must block, got %v", d.Verdict)
	}

	// Pipeline treats classifier errors as allow (fail-open at chain level).
	p := NewPipeline(open)
	if v := p.Check(context.Background(), "app", &domain.Message{Text: "x"}); v.Verdict != VerdictAllow {
		t.Fatalf("pipeline fail-open, got %v", v.Verdict)
	}
}
