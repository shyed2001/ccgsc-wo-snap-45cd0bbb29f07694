// Path: cmd/pools-watcher/browser.go | WO F16 (cmdc)
// What/why: the real BrowserLauncher. Each Launch starts a headless Chrome
// process and loads the contribute page URL; each Close cancels that Chrome
// process, so no child process survives the watcher.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/chromedp/chromedp"
)

// chromeLauncher launches headless Chrome contributor tabs.
type chromeLauncher struct {
	chrome string
}

// chromeTab is one launched Chrome process.
type chromeTab struct {
	cancel context.CancelFunc
}

// newChromeLauncher resolves the Chrome binary and answers the launcher.
func newChromeLauncher(chrome string) (*chromeLauncher, error) {
	path, err := chromePath(chrome)
	if err != nil {
		return nil, err
	}
	return &chromeLauncher{chrome: path}, nil
}

// Launch starts one headless Chrome on the contribute page and answers a handle
// that closes it.
func (l *chromeLauncher) Launch(ctx context.Context, url string) (TabHandle, error) {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if l.chrome != "" {
		opts = append(opts, chromedp.ExecPath(l.chrome))
	}
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
	if _, err := chromedp.Run(browserCtx, chromedp.Navigate(url)); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("launch the contributor tab: %w", err)
	}
	return &chromeTab{cancel: func() {
		browserCancel()
		allocCancel()
	}}, nil
}

// Close closes one tab, killing its Chrome process.
func (l *chromeLauncher) Close(h TabHandle) {
	t, ok := h.(*chromeTab)
	if !ok || t == nil || t.cancel == nil {
		return
	}
	t.cancel()
	t.cancel = nil
}

// chromePath answers the Chrome binary to use: the -chrome flag when it is
// set, otherwise the binary found on the system.
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
