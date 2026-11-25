// Path: cmd/pools-browser-smoke/browser.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the headless Chrome driver of the harness. It starts a real
// headless Chrome, loads the contributor page and the runner page, and reads
// the state the page already writes: the status and log text of the page, the
// console messages of the browser and the CSP of the served runner page.
//
// The harness never adds a test hook to the platform code. Everything it
// asserts is read from the DOM the page already fills, from the console the
// browser already prints, or from the HTTP answer of the server.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// browser is the headless Chrome of one harness run.
type browser struct {
	// console holds every console message and log entry the browser
	// reported, in arrival order.
	console []chromedp.ConsoleMessage
	// cancelCtx ends the browser: the Chrome process dies with it.
	cancelCtx context.CancelFunc
	// ctx is the chromedp context of the run.
	ctx context.Context
}

// chromePath answers the Chrome binary to use: the -chrome flag when it is
// set, otherwise the binary chromedp finds itself.
func chromePath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		path := strings.TrimSpace(explicit)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("the Chrome binary %s does not exist: %w", path, err)
		}
		return path, nil
	}
	path, err := exec.LookPath("chrome.exe")
	if err == nil {
		return path, nil
	}
	for _, candidate := range []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no Chrome binary found: pass -chrome <path>")
}

// startBrowser starts the headless Chrome and answers the driver.
func startBrowser(ctx context.Context, chrome string) (*browser, error) {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if chrome != "" {
		opts = append(opts, chromedp.ExecPath(chrome))
	}
	// A headless run has no window and no GPU: the sandbox checks the DOM
	// and the console, not the rendering.
	opts = append(opts,
		chromedp.Flag("headless", "new"),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
		chromedp.WindowSize(1280, 900),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	driver := &browser{cancelCtx: func() {
		browserCancel()
		allocCancel()
	}}
	// The console subscription must exist BEFORE the first navigation, so no
	// message of the page is lost. chromedp.Console collects the console API
	// calls AND the log entries of the browser (Log.entryAdded), which is
	// exactly what the WO asks for.
	messages := chromedp.Console(browserCtx)
	go func() {
		for msg, err := range messages {
			if err != nil {
				return
			}
			driver.console = append(driver.console, msg)
		}
	}()
	// The browser is started now, so a broken Chrome fails here and not in
	// the middle of a check.
	if err := chromedp.Do(browserCtx); err != nil {
		driver.cancelCtx()
		return nil, fmt.Errorf("start the headless browser: %w", err)
	}
	driver.ctx = browserCtx
	return driver, nil
}

// Close stops the browser. The Chrome process dies with the context.
func (b *browser) Close() {
	if b == nil || b.cancelCtx == nil {
		return
	}
	b.cancelCtx()
	b.cancelCtx = nil
}

// consoleMessages answers every console message and log entry seen so far.
func (b *browser) consoleMessages() []chromedp.ConsoleMessage {
	out := make([]chromedp.ConsoleMessage, len(b.console))
	copy(out, b.console)
	return out
}

// cspViolations answers the CSP violations the browser reported. A CSP
// violation arrives as a console message of the error level and, in Chrome, as
// a log entry of the security source.
func (b *browser) cspViolations() []string {
	out := []string{}
	for _, msg := range b.consoleMessages() {
		if isCSPViolation(msg) {
			out = append(out, strings.TrimSpace(msg.Text))
		}
	}
	return out
}

// isCSPViolation reports whether one console message is a CSP violation.
func isCSPViolation(msg chromedp.ConsoleMessage) bool {
	text := msg.Text
	if !strings.Contains(text, "Content Security Policy") &&
		!strings.Contains(text, "Refused to") {
		return false
	}
	return msg.Type == chromedp.ConsoleError ||
		msg.Type == chromedp.ConsoleException ||
		msg.Source == string(log.EntrySourceSecurity) ||
		msg.Source == string(log.EntrySourceViolation)
}

// pageState is the state the contributor page writes into its DOM.
type pageState struct {
	// Log is the text of the log box of the page.
	Log string
	// StatusBadge is the text of the status badge.
	StatusBadge string
	// SourceBadge is the text of the source badge.
	SourceBadge string
	// LogLines are the single log lines of the page, newest first.
	LogLines []string
}

// readPageState reads the status and the log text the page already writes.
func (b *browser) readPageState(ctx context.Context) (pageState, error) {
	// The expression answers a plain object, and the object is read as raw
	// JSON, so the field names of the page are mapped here.
	raw, err := chromedp.Run(b.ctx,
		chromedp.Evaluate[[]byte](`(() => {
			const log = document.getElementById('log-output');
			const status = document.getElementById('status-badge');
			const source = document.getElementById('source-badge');
			return {
				log: log ? log.textContent : '',
				status: status ? status.textContent : '',
				source: source ? source.textContent : ''
			};
		})()`),
	)
	if err != nil {
		return pageState{}, fmt.Errorf("read the page state: %w", err)
	}
	var st pageState
	if err := json.Unmarshal(raw, &st); err != nil {
		return pageState{}, fmt.Errorf("read the page state: %w", err)
	}
	for _, line := range strings.Split(st.Log, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			st.LogLines = append(st.LogLines, line)
		}
	}
	return st, nil
}

// waitForLogLine waits until the log of the page carries a line that carries
// the needle. It answers the line and whether it arrived.
func (b *browser) waitForLogLine(ctx context.Context, needle string, budget time.Duration) (string, bool) {
	deadline := time.Now().Add(budget)
	for {
		st, err := b.readPageState(ctx)
		if err == nil {
			for _, line := range st.LogLines {
				if strings.Contains(line, needle) {
					return line, true
				}
			}
		}
		if time.Now().After(deadline) {
			return "", false
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// navigate loads a URL and waits for the document.
func (b *browser) navigate(ctx context.Context, url string) error {
	if _, err := chromedp.Run(b.ctx, chromedp.Navigate(url)); err != nil {
		return fmt.Errorf("load %s: %w", url, err)
	}
	return nil
}

// waitDocument waits until the document of the page is complete.
func (b *browser) waitDocument(ctx context.Context, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	for {
		ready, err := chromedp.Run(b.ctx, chromedp.Evaluate[string]("document.readyState"))
		if err == nil && (ready == "complete" || ready == "interactive") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the document did not become ready within %s", budget)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// fetchPage reads a URL over the HTTP client of the harness, not over the
// browser: the CSP header and the CSP meta of the page are server facts.
func fetchPage(ctx context.Context, url string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, nil, err
	}
	return body, res.Header, nil
}

// runtimeExceptions answers the uncaught exceptions the page threw.
func (b *browser) runtimeExceptions() []string {
	out := []string{}
	for _, msg := range b.consoleMessages() {
		if msg.Type == chromedp.ConsoleException {
			out = append(out, strings.TrimSpace(msg.Text))
		}
	}
	return out
}

// consoleAPICalled answers the raw console events, for the report.
func consoleAPICalled(events []chromedp.ConsoleMessage) int {
	n := 0
	for _, ev := range events {
		if ev.Type == chromedp.ConsoleLog || ev.Type == chromedp.ConsoleWarning ||
			ev.Type == chromedp.ConsoleInfo || ev.Type == chromedp.ConsoleError {
			n++
		}
	}
	return n
}

// logEntries answers the raw browser log entries, for the report.
func logEntries(events []chromedp.ConsoleMessage) int {
	n := 0
	for _, ev := range events {
		if ev.Source == string(log.EntrySourceSecurity) || ev.Source == string(log.EntrySourceViolation) ||
			ev.Source == string(log.EntrySourceJavascript) || ev.Source == string(log.EntrySourceWorker) {
			n++
		}
	}
	return n
}

// ensure the cdproto runtime package stays imported: the console events of
// the browser carry the runtime exception type, and the import documents the
// protocol types the harness relies on.
var _ = runtime.EventConsoleAPICalled{}

// workerPaths answers the paths of the workload files the harness uploads as
// the module artifact of the job.
func workloadPaths(root string) (string, string, error) {
	kernel := filepath.Join(root, "web", "workloads", "matmul", "matmul_kernel.js")
	manifest := filepath.Join(root, "web", "workloads", "matmul", "manifest.json")
	for _, p := range []string{kernel, manifest} {
		if _, err := os.Stat(p); err != nil {
			return "", "", fmt.Errorf("the reference workload is missing: %w", err)
		}
	}
	return kernel, manifest, nil
}
