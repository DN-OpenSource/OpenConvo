package moderation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/openstream/openstream/internal/domain"
)

// WebhookClassifier POSTs the message to an external endpoint that decides
// the verdict (SPEC.md §11.2 — generic hook, not AI-specific). The default
// budget is 200ms; on timeout/error the middleware fails open unless
// FailClosed is set.
type WebhookClassifier struct {
	URL        string
	Timeout    time.Duration
	FailClosed bool
	Client     *http.Client
}

type classifierRequest struct {
	AppID   string          `json:"app_id"`
	Message *domain.Message `json:"message"`
}

type classifierResponse struct {
	Verdict string `json:"verdict"` // allow | flag | block | shadow
	Reason  string `json:"reason"`
}

// Check implements Middleware.
func (w *WebhookClassifier) Check(ctx context.Context, appID string, msg *domain.Message) (Decision, error) {
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(classifierRequest{AppID: appID, Message: msg})
	if err != nil {
		return Decision{Verdict: VerdictAllow}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return Decision{Verdict: VerdictAllow}, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return w.failMode(fmt.Errorf("moderation: classifier: %w", err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return w.failMode(fmt.Errorf("moderation: classifier status %d", resp.StatusCode))
	}
	var decoded classifierResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return w.failMode(fmt.Errorf("moderation: classifier decode: %w", err))
	}
	switch Verdict(decoded.Verdict) {
	case VerdictFlag, VerdictBlock, VerdictShadow:
		return Decision{Verdict: Verdict(decoded.Verdict), Reason: "classifier:" + decoded.Reason}, nil
	default:
		return Decision{Verdict: VerdictAllow}, nil
	}
}

func (w *WebhookClassifier) failMode(err error) (Decision, error) {
	if w.FailClosed {
		return Decision{Verdict: VerdictBlock, Reason: "classifier unavailable (fail-closed)"}, nil
	}
	return Decision{Verdict: VerdictAllow}, err
}
