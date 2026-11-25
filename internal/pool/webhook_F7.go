// Path: internal/pool/webhook_F7.go | Feature: T42-F7
package pool

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errWebhookURL = errors.New("invalid webhook URL")
var errWebhookPermanent = errors.New("permanent webhook failure")

func IsPermanentWebhookError(err error) bool {
	return errors.Is(err, errWebhookPermanent)
}

func ValidateWebhookURL(raw string, allowPrivate bool) error {
	if len(raw) > 2048 {
		return errWebhookURL
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Fragment != "" {
		return errWebhookURL
	}
	if u.Scheme != "https" && !(allowPrivate && u.Scheme == "http") {
		return errWebhookURL
	}
	if !allowPrivate {
		ips, err := net.LookupIP(u.Hostname())
		if err != nil || len(ips) == 0 {
			return fmt.Errorf("%w: host resolution failed", errWebhookURL)
		}
		for _, ip := range ips {
			if isPrivateOrLocalIP(ip) {
				return fmt.Errorf("%w: private or local address", errWebhookURL)
			}
		}
	}
	return nil
}

func isPrivateOrLocalIP(ip net.IP) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() || (cgnat != nil && cgnat.Contains(ip))
}

type WebhookJobView struct {
	Event      string    `json:"event"`
	DeliveryID string    `json:"deliveryId"`
	JobID      string    `json:"jobId"`
	PoolID     string    `json:"poolId"`
	State      string    `json:"state"`
	At         time.Time `json:"at"`
	Progress   Progress  `json:"progress"`
	ResultsURL string    `json:"resultsUrl,omitempty"`
	BulkURL    string    `json:"bulkUrl,omitempty"`
}

type WebhookPayload struct {
	WebhookJobView
}

func WebhookBody(j Job, deliveryID string, event ...string) ([]byte, error) {
	return WebhookBodyAt(j, deliveryID, time.Now().UTC(), event...)
}

func WebhookBodyAt(j Job, deliveryID string, at time.Time, event ...string) ([]byte, error) {
	kind := "job.done"
	if len(event) > 0 && event[0] != "" {
		kind = event[0]
	}
	return json.Marshal(WebhookPayload{WebhookJobView: WebhookJobView{
		Event: kind, DeliveryID: deliveryID, JobID: j.JobID, PoolID: j.PoolID,
		State: j.State, At: at, Progress: j.Progress,
	}})
}

func WebhookSignature(secret string, body []byte, parts ...string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	if len(parts) > 0 {
		_, _ = mac.Write([]byte(parts[0]))
		_, _ = mac.Write([]byte("."))
	}
	_, _ = mac.Write(body)
	sum := hex.EncodeToString(mac.Sum(nil))
	if len(parts) == 0 {
		return "sha256=" + sum
	}
	return sum
}

func WebhookRequest(ctx context.Context, client *http.Client, webhook WebhookConfig, j Job, deliveryID string, attempt int, timestamp time.Time, event ...string) error {
	body, err := WebhookBody(j, deliveryID, event...)
	if err != nil {
		return err
	}
	return webhookRequestBody(ctx, client, webhook, body, deliveryID, attempt, timestamp)
}

func webhookRequestBody(ctx context.Context, client *http.Client, webhook WebhookConfig, body []byte, deliveryID string, attempt int, timestamp time.Time) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(timestamp.Unix(), 10)
	signature := WebhookSignature(webhook.Secret, body, ts)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WWPP-Signature", signature)
	req.Header.Set("X-WWPP-Timestamp", ts)
	req.Header.Set("X-WWPP-Delivery", deliveryID)
	req.Header.Set("X-WWPP-Attempt", strconv.Itoa(attempt))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
		if resp.StatusCode >= 500 {
			return fmt.Errorf("webhook retryable HTTP %d; retry-after=%s", resp.StatusCode, resp.Header.Get("Retry-After"))
		}
		return fmt.Errorf("%w: webhook returned HTTP %d", errWebhookPermanent, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("webhook retryable HTTP %d; retry-after=%s", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	return nil
}

func DeliverWebhook(ctx context.Context, webhook WebhookConfig, j Job, allowPrivateIPs bool, eventName ...string) error {
	event := ""
	if len(eventName) > 0 {
		event = eventName[0]
	}
	deliveryID := fmt.Sprintf("%s-%d", j.JobID, time.Now().UnixNano())
	var payload []byte
	if len(eventName) > 1 && eventName[1] != "" {
		deliveryID = eventName[1]
	}
	if len(eventName) > 2 {
		payload, _ = hex.DecodeString(eventName[2])
	}
	u, err := url.Parse(webhook.URL)
	if err != nil {
		return err
	}
	client := webhookHTTPClient(allowPrivateIPs)
	if u.Hostname() == "" {
		return errWebhookURL
	}
	var last error
	delays := []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute}
	for attempt := 1; attempt <= 5; attempt++ {
		if attempt > 1 {
			delay := delays[attempt-2]
			if retryAfter := retryAfterDelay(last); retryAfter >= 0 {
				delay = retryAfter
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		err := deliverWebhookAttemptWithClient(ctx, client, webhook, j, deliveryID, attempt, payload, event)
		if err == nil {
			return nil
		}
		if errors.Is(err, errWebhookPermanent) {
			return err
		}
		last = err
	}
	return last
}

// DeliverWebhookAttempt performs exactly one HTTP delivery. The API outbox owns
// retry scheduling so a claimed delivery never occupies a semaphore for backoff.
func DeliverWebhookAttempt(ctx context.Context, webhook WebhookConfig, j Job, allowPrivateIPs bool, attempt int, eventName ...string) error {
	event := ""
	if len(eventName) > 0 {
		event = eventName[0]
	}
	deliveryID := fmt.Sprintf("%s-%d", j.JobID, time.Now().UnixNano())
	if len(eventName) > 1 && eventName[1] != "" {
		deliveryID = eventName[1]
	}
	var payload []byte
	if len(eventName) > 2 {
		payload, _ = hex.DecodeString(eventName[2])
	}
	client := webhookHTTPClient(allowPrivateIPs)
	return deliverWebhookAttemptWithClient(ctx, client, webhook, j, deliveryID, attempt, payload, event)
}

func deliverWebhookAttemptWithClient(ctx context.Context, client *http.Client, webhook WebhookConfig, j Job, deliveryID string, attempt int, payload []byte, event string) error {
	if len(payload) > 0 {
		return webhookRequestBody(ctx, client, webhook, payload, deliveryID, attempt, time.Now().UTC())
	}
	return WebhookRequest(ctx, client, webhook, j, deliveryID, attempt, time.Now().UTC(), event)
}

func webhookHTTPClient(allowPrivateIPs bool) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				ips, err := net.LookupIP(host)
				if err != nil || len(ips) == 0 {
					return nil, fmt.Errorf("%w: host resolution failed", errWebhookURL)
				}
				for _, ip := range ips {
					if !allowPrivateIPs && isPrivateOrLocalIP(ip) {
						return nil, fmt.Errorf("%w: private or local address", errWebhookURL)
					}
				}
				var last error
				for _, ip := range ips {
					conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
					if err == nil {
						return conn, nil
					}
					last = err
				}
				return nil, last
			},
		},
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("%w: redirects are not allowed", errWebhookURL)
		},
	}
}

func lastErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func retryAfterDelay(err error) time.Duration {
	text := lastErrorText(err)
	const marker = "retry-after="
	i := strings.Index(text, marker)
	if i < 0 {
		return -1
	}
	value := strings.TrimSpace(strings.TrimSuffix(text[i+len(marker):], ")"))
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func (w WebhookConfig) Validate(allowPrivate bool) error {
	if strings.TrimSpace(w.URL) == "" || len(w.Secret) < 32 || len(w.Secret) > 512 {
		return errWebhookURL
	}
	return ValidateWebhookURL(w.URL, allowPrivate)
}
