// Path: internal/pool/upload_scan_F8.go | Author: hermes (MiniMax-M3) for the owner | 2026-11-18
// What/why: T42 F8 module upload scan (size, WASM magic, import allow-list, shared-memory
// rejection) plus the admin review gate state of an uploaded artifact.
// Sources: WWPP-R36.1 (upload checks on developer code), R32.7 (content-addressed upload),
// R12.1 (review quorum 1-of-2 for MVP 100, 2-of-2 for MVP 200), R13.1 (owner code exempt from
// the quorum), R10.0 (no SharedArrayBuffer / shared WebAssembly.Memory, now or later).
package pool

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/store"
)

// Scan severities. Only an error finding rejects the upload.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Review states of an uploaded artifact (WWPP-R36.1 review gate).
const (
	ReviewNotRequired = "not-required"
	ReviewPending     = "pending"
	ReviewApproved    = "approved"
	ReviewRejected    = "rejected"
)

var (
	ErrArtifactRejected = errors.New("artifact rejected")
	ErrArtifactPending  = errors.New("artifact pending")
)

// Review decisions accepted by the admin review route.
const (
	ReviewDecisionApprove = "approve"
	ReviewDecisionReject  = "reject"
)

// Scan finding codes.
const (
	FindingSizeExceeded     = "size-exceeded"
	FindingWasmMagicMissing = "wasm-magic-missing"
	FindingWasmMalformed    = "wasm-malformed"
	FindingImportNotAllowed = "import-not-allowed"
	FindingSharedMemory     = "shared-memory"
	FindingEmptyModule      = "empty-module"
	FindingWasmVersion      = "wasm-version"
	FindingWasmSectionOrder = "wasm-section-order"
	FindingWasmMemoryLimit  = "wasm-memory-limit"
	FindingJSRemoteImport   = "js-remote-import"
	FindingZipEntry         = "zip-entry"
	FindingZipEntries       = "zip-entries"
	FindingZipUncompressed  = "zip-uncompressed"
	FindingWasmMagic        = "wasm-magic"
)

// uploadScanConfigKey is the durable key of the scan configuration in the metrics bucket.
const uploadScanConfigKey = "upload_scan_config"

// ScanFinding is one result of the upload scan.
type ScanFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
}

// ScanReport is the stored result of one upload scan.
type ScanReport struct {
	ArtifactID string        `json:"artifactId"`
	Kind       string        `json:"kind"`
	SizeBytes  int64         `json:"sizeBytes"`
	SHA256     string        `json:"sha256,omitempty"`
	OK         bool          `json:"ok"`
	Imports    []string      `json:"imports,omitempty"`
	Findings   []ScanFinding `json:"findings,omitempty"`
	ScannedAt  time.Time     `json:"scannedAt"`
}

// UploadScanConfig holds the admin switches of the upload scan (WWPP-R36.1).
type UploadScanConfig struct {
	// Enabled runs the scan when an artifact completes.
	Enabled bool `json:"enabled"`
	// MaxBytes is the size ceiling of one uploaded module.
	MaxBytes int64 `json:"maxBytes"`
	// RequireWasmMagic rejects an artifact that declares application/wasm but does
	// not carry the WASM magic bytes.
	RequireWasmMagic bool `json:"requireWasmMagic"`
	// AllowedImports is the import allow-list. An entry is an exact "module.field"
	// or a "module.*" prefix rule. "*" allows every import.
	AllowedImports []string `json:"allowedImports"`
	// AllowSharedMemory permits shared WebAssembly.Memory. The owner decision
	// R10.0 forbids it, so the default is false.
	AllowSharedMemory bool `json:"allowSharedMemory"`
	// RequireReview sends every accepted upload into the admin review queue.
	RequireReview bool `json:"requireReview"`
	// ReviewQuorum is the number of distinct admin approvals an artifact needs.
	ReviewQuorum       int    `json:"reviewQuorum"`
	MaxWASMBytes       int64  `json:"maxWasmBytes"`
	MaxJSBytes         int64  `json:"maxJsBytes"`
	MaxJSONBytes       int64  `json:"maxJsonBytes"`
	MaxZipEntries      int    `json:"maxZipEntries"`
	MaxZipUncompressed int64  `json:"maxZipUncompressed"`
	MaxWASMPages       uint64 `json:"maxWasmPages"`
}

// DefaultUploadScanConfig returns the F8 defaults: the scan runs, the size ceiling is
// the transport ceiling, the WASI host functions are allowed, shared memory is refused
// (R10.0) and the review gate is off until an admin turns it on.
func DefaultUploadScanConfig() UploadScanConfig {
	return UploadScanConfig{
		Enabled:            true,
		MaxBytes:           MaxArtifactBytes,
		RequireWasmMagic:   true,
		AllowedImports:     []string{"env.memory", "env.table", "env.abort", "env.log", "env.logf", "env.now", "env.random_get", "env.memcpy", "env.memset"},
		AllowSharedMemory:  false,
		RequireReview:      false,
		ReviewQuorum:       1,
		MaxWASMBytes:       16 << 20,
		MaxJSBytes:         4 << 20,
		MaxJSONBytes:       1 << 20,
		MaxZipEntries:      256,
		MaxZipUncompressed: 64 << 20,
		MaxWASMPages:       4096,
	}
}

// normalize fills the unset fields of a stored configuration. It is used when a
// durable record is loaded, so a record written by an older version keeps working.
// A stored allow-list of nil means "the field was never written"; an empty but
// non-nil list means "no import is allowed".
func (c UploadScanConfig) normalize() UploadScanConfig {
	d := DefaultUploadScanConfig()
	if c.MaxBytes <= 0 {
		c.MaxBytes = d.MaxBytes
	}
	if c.AllowedImports == nil {
		c.AllowedImports = d.AllowedImports
	}
	if c.ReviewQuorum <= 0 {
		c.ReviewQuorum = d.ReviewQuorum
	}
	if c.MaxWASMBytes <= 0 {
		c.MaxWASMBytes = d.MaxWASMBytes
	}
	if c.MaxJSBytes <= 0 {
		c.MaxJSBytes = d.MaxJSBytes
	}
	if c.MaxJSONBytes <= 0 {
		c.MaxJSONBytes = d.MaxJSONBytes
	}
	if c.MaxZipEntries <= 0 {
		c.MaxZipEntries = d.MaxZipEntries
	}
	if c.MaxZipUncompressed <= 0 {
		c.MaxZipUncompressed = d.MaxZipUncompressed
	}
	if c.MaxWASMPages == 0 {
		c.MaxWASMPages = d.MaxWASMPages
	}
	return c
}

// validate rejects a configuration that the transport or the review gate cannot honor.
// The setter never normalizes an explicit value: an invalid value is an error, never
// a silent correction.
func (c UploadScanConfig) validate() error {
	if c.MaxBytes <= 0 || c.MaxBytes > MaxArtifactBytes {
		return fmt.Errorf("%w: upload scan maxBytes must be 1..%d", ErrInvalid, MaxArtifactBytes)
	}
	if c.ReviewQuorum < 1 || c.ReviewQuorum > 10 {
		return fmt.Errorf("%w: upload scan reviewQuorum must be 1..10", ErrInvalid)
	}
	if c.MaxWASMBytes <= 0 || c.MaxWASMBytes > MaxArtifactBytes ||
		c.MaxJSBytes <= 0 || c.MaxJSBytes > MaxArtifactBytes ||
		c.MaxJSONBytes <= 0 || c.MaxJSONBytes > MaxArtifactBytes ||
		c.MaxZipEntries < 1 || c.MaxZipEntries > 10000 ||
		c.MaxZipUncompressed <= 0 || c.MaxZipUncompressed > 64<<20 ||
		c.MaxWASMPages == 0 || c.MaxWASMPages > 65536 {
		return fmt.Errorf("%w: invalid upload scan kind limits", ErrInvalid)
	}
	for _, rule := range c.AllowedImports {
		if rule == "" {
			return fmt.Errorf("%w: upload scan allowedImports has an empty rule", ErrInvalid)
		}
		if strings.Contains(rule, "*") {
			return fmt.Errorf("%w: upload scan allowedImports rule %q must be exact", ErrInvalid, rule)
		}
	}
	return nil
}

// uploadScanState keeps the active configuration of one engine outside the engine
// struct, so the feature file owns its own state.
type uploadScanState struct {
	mu  sync.RWMutex
	cfg UploadScanConfig
}

var uploadScanStates sync.Map // *Engine -> *uploadScanState

func (e *Engine) uploadScanState() *uploadScanState {
	if v, ok := uploadScanStates.Load(e); ok {
		return v.(*uploadScanState)
	}
	st := &uploadScanState{cfg: DefaultUploadScanConfig()}
	actual, _ := uploadScanStates.LoadOrStore(e, st)
	return actual.(*uploadScanState)
}

// UploadScanConfig returns the active upload scan configuration.
func (e *Engine) UploadScanConfig() UploadScanConfig {
	st := e.uploadScanState()
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.cfg
}

func (e *Engine) setUploadScanConfig(c UploadScanConfig) {
	st := e.uploadScanState()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.cfg = c
}

// loadUploadScanConfig reads the durable configuration. It runs outside any
// transaction, because the store allows one connection.
func (e *Engine) loadUploadScanConfig() error {
	var c UploadScanConfig
	err := e.Store.View(context.Background(), func(r store.Reader) error {
		return r.Get(store.Metrics, uploadScanConfigKey, &c)
	})
	if errors.Is(err, store.ErrNotFound) {
		e.setUploadScanConfig(DefaultUploadScanConfig())
		return nil
	}
	if err != nil {
		return err
	}
	c = c.normalize()
	if err = c.validate(); err != nil {
		return err
	}
	e.setUploadScanConfig(c)
	return nil
}

// SetUploadScanConfig stores a new configuration and applies it to the engine.
// An invalid value is rejected, never corrected: the admin sees the error.
func (e *Engine) SetUploadScanConfig(ctx context.Context, c UploadScanConfig) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := e.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.Metrics, uploadScanConfigKey, c)
	}); err != nil {
		return err
	}
	e.setUploadScanConfig(c)
	return nil
}

// wasmMagic is the WASM binary magic number.
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6D}

// looksLikeWASM reports whether the bytes carry the WASM magic number.
func looksLikeWASM(data []byte) bool {
	return len(data) >= len(wasmMagic) && bytes.Equal(data[:len(wasmMagic)], wasmMagic)
}

// wasmReader reads the little endian base 128 values of a WASM binary.
type wasmReader struct {
	b []byte
	i int
}

func (r *wasmReader) byte() (byte, error) {
	if r.i >= len(r.b) {
		return 0, errors.New("unexpected end of wasm binary")
	}
	v := r.b[r.i]
	r.i++
	return v, nil
}

func (r *wasmReader) bytes(n int) ([]byte, error) {
	if n < 0 || r.i+n > len(r.b) {
		return nil, errors.New("unexpected end of wasm section")
	}
	out := r.b[r.i : r.i+n]
	r.i += n
	return out, nil
}

// u64 reads one unsigned little endian base 128 value.
func (r *wasmReader) u64() (uint64, error) {
	var result uint64
	var shift uint
	for i := 0; i < 10; i++ {
		b, err := r.byte()
		if err != nil {
			return 0, err
		}
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
	}
	return 0, errors.New("wasm leb128 value overflows")
}

func (r *wasmReader) name() (string, error) {
	n, err := r.u64()
	if err != nil {
		return "", err
	}
	b, err := r.bytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// limits reads one limits value and reports whether the memory is shared.
func (r *wasmReader) limits() (bool, error) {
	flags, err := r.byte()
	if err != nil {
		return false, err
	}
	if _, err = r.u64(); err != nil {
		return false, err
	}
	if flags&0x01 != 0 {
		if _, err = r.u64(); err != nil {
			return false, err
		}
	}
	return flags&0x02 != 0, nil
}

// wasmModule is the part of a WASM binary the upload scan needs.
type wasmModule struct {
	imports  []string
	shared   bool
	maxPages uint64
	start    bool
}

// parseWASM reads the import section and the memory section of a WASM binary.
func parseWASM(data []byte) (wasmModule, error) {
	var m wasmModule
	r := &wasmReader{b: data}
	if _, err := r.bytes(len(wasmMagic)); err != nil {
		return m, err
	}
	version, err := r.bytes(4)
	if err != nil {
		return m, err
	}
	if !bytes.Equal(version, []byte{1, 0, 0, 0}) {
		return m, fmt.Errorf("%s", FindingWasmVersion)
	}
	lastSection := byte(0)
	seen := map[byte]bool{}
	for r.i < len(r.b) {
		id, err := r.byte()
		if err != nil {
			return m, err
		}
		size, err := r.u64()
		if err != nil {
			return m, err
		}
		payload, err := r.bytes(int(size))
		if err != nil {
			return m, err
		}
		if id != 0 {
			if id > 12 || seen[id] || (lastSection != 0 && id < lastSection) {
				return m, fmt.Errorf("%s", FindingWasmSectionOrder)
			}
			seen[id] = true
			lastSection = id
		}
		switch id {
		case 2: // import section
			p := &wasmReader{b: payload}
			count, err := p.u64()
			if err != nil {
				return m, err
			}
			for i := uint64(0); i < count; i++ {
				module, err := p.name()
				if err != nil {
					return m, err
				}
				field, err := p.name()
				if err != nil {
					return m, err
				}
				kind, err := p.byte()
				if err != nil {
					return m, err
				}
				m.imports = append(m.imports, module+"."+field)
				switch kind {
				case 0x00: // function
					if _, err = p.u64(); err != nil {
						return m, err
					}
				case 0x01: // table
					if _, err = p.byte(); err != nil {
						return m, err
					}
					if _, err = p.limits(); err != nil {
						return m, err
					}
				case 0x02: // memory
					flags, err := p.byte()
					if err != nil {
						return m, err
					}
					min, err := p.u64()
					if err != nil {
						return m, err
					}
					if min > m.maxPages {
						m.maxPages = min
					}
					if flags&0x01 != 0 {
						max, err := p.u64()
						if err != nil {
							return m, err
						}
						if max > m.maxPages {
							m.maxPages = max
						}
					}
					if flags&0x02 != 0 {
						m.shared = true
					}
				case 0x03: // global
					if _, err = p.bytes(2); err != nil {
						return m, err
					}
				case 0x04: // tag
					if _, err = p.byte(); err != nil {
						return m, err
					}
					if _, err = p.u64(); err != nil {
						return m, err
					}
				default:
					return m, fmt.Errorf("unknown wasm import kind %d", kind)
				}
			}
		case 5: // memory section
			p := &wasmReader{b: payload}
			count, err := p.u64()
			if err != nil {
				return m, err
			}
			for i := uint64(0); i < count; i++ {
				flags, err := p.byte()
				if err != nil {
					return m, err
				}
				min, err := p.u64()
				if err != nil {
					return m, err
				}
				if min > m.maxPages {
					m.maxPages = min
				}
				if flags&0x01 != 0 {
					max, err := p.u64()
					if err != nil {
						return m, err
					}
					if max > m.maxPages {
						m.maxPages = max
					}
				}
				if flags&0x02 != 0 {
					m.shared = true
				}
			}
		case 8:
			m.start = true
		}
	}
	return m, nil
}

// importAllowed reports whether one import passes the allow-list.
func importAllowed(rules []string, imp string) bool {
	for _, rule := range rules {
		if rule == imp {
			return true
		}
	}
	return false
}

var jsRemoteImportRE = regexp.MustCompile(`(?i)(?:import\s*(?:[^;]*?\sfrom\s*)?|export\s+[^;]*?\sfrom\s*|importScripts\s*\(\s*|import\s*\(\s*)["'](?:https?:|//|ws:|wss:|data:|blob:|javascript:|file:)`)

func scanJavaScript(data []byte) []ScanFinding {
	if jsRemoteImportRE.Match(data) {
		return []ScanFinding{{Code: FindingJSRemoteImport, Severity: SeverityError, Message: "remote JavaScript imports are not allowed."}}
	}
	return nil
}

const maxNestedZipDepth = 8

type zipScanBudget struct {
	entries      int
	uncompressed uint64
}

func (e *Engine) scanZip(cfg UploadScanConfig, data []byte) []ScanFinding {
	return e.scanZipWithBudget(cfg, data, 0, &zipScanBudget{})
}

func (e *Engine) scanZipWithBudget(cfg UploadScanConfig, data []byte, depth int, budget *zipScanBudget) []ScanFinding {
	if depth > maxNestedZipDepth {
		return []ScanFinding{{Code: FindingZipEntry, Severity: SeverityError, Message: "The ZIP archive nesting depth exceeds the safety limit."}}
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return []ScanFinding{{Code: FindingZipEntry, Severity: SeverityError, Message: "The ZIP archive could not be read.", Detail: err.Error()}}
	}
	findings := []ScanFinding{}
	for _, f := range r.File {
		budget.entries++
		if budget.entries > cfg.MaxZipEntries {
			findings = append(findings, ScanFinding{Code: FindingZipEntries, Severity: SeverityError, Message: "The ZIP archive has too many entries."})
			break
		}
		if strings.Contains(f.Name, "..") || strings.HasPrefix(f.Name, "/") {
			findings = append(findings, ScanFinding{Code: FindingZipEntry, Severity: SeverityError, Message: "The ZIP archive contains an unsafe path.", Detail: f.Name})
			continue
		}
		if f.UncompressedSize64 > uint64(cfg.MaxZipUncompressed) ||
			budget.uncompressed > uint64(cfg.MaxZipUncompressed)-f.UncompressedSize64 {
			findings = append(findings, ScanFinding{Code: FindingZipUncompressed, Severity: SeverityError, Message: "The ZIP archive exceeds the uncompressed size ceiling."})
			break
		}
		budget.uncompressed += f.UncompressedSize64
		rc, er := f.Open()
		if er != nil {
			findings = append(findings, ScanFinding{Code: FindingZipEntry, Severity: SeverityError, Message: "The ZIP entry could not be read.", Detail: f.Name})
			continue
		}
		remaining := uint64(cfg.MaxZipUncompressed) - budget.uncompressed + f.UncompressedSize64
		entry, er := io.ReadAll(io.LimitReader(rc, int64(remaining)+1))
		_ = rc.Close()
		if er != nil {
			findings = append(findings, ScanFinding{Code: FindingZipEntry, Severity: SeverityError, Message: "The ZIP entry could not be read.", Detail: f.Name})
			continue
		}
		if looksLikeWASM(entry) || strings.HasSuffix(strings.ToLower(f.Name), ".wasm") {
			var module wasmModule
			if module, er = parseWASM(entry); er != nil {
				findings = append(findings, ScanFinding{Code: FindingZipEntry, Severity: SeverityError, Message: "A ZIP entry failed WASM validation.", Detail: f.Name})
			} else {
				for _, imp := range module.imports {
					if !importAllowed(cfg.AllowedImports, imp) {
						findings = append(findings, ScanFinding{Code: FindingImportNotAllowed, Severity: SeverityError, Message: "The module imports a host function that is not on the allow-list.", Detail: f.Name + ": " + imp})
					}
				}
				if module.shared && !cfg.AllowSharedMemory {
					findings = append(findings, ScanFinding{Code: FindingSharedMemory, Severity: SeverityError, Message: "Shared WebAssembly.Memory is not allowed (R10.0).", Detail: f.Name})
				}
				if module.maxPages > cfg.MaxWASMPages {
					findings = append(findings, ScanFinding{Code: FindingWasmMemoryLimit, Severity: SeverityError, Message: "The WASM memory limit exceeds the configured ceiling.", Detail: f.Name})
				}
			}
		} else if strings.HasSuffix(strings.ToLower(f.Name), ".zip") {
			findings = append(findings, e.scanZipWithBudget(cfg, entry, depth+1, budget)...)
		} else if strings.HasSuffix(strings.ToLower(f.Name), ".js") {
			findings = append(findings, scanJavaScript(entry)...)
		}
	}
	return findings
}

// scanBytes runs the upload scan over the bytes of an artifact.
func (e *Engine) scanBytes(cfg UploadScanConfig, a Artifact) ScanReport {
	report := ScanReport{ArtifactID: a.ArtifactID, SizeBytes: int64(len(a.Data)), SHA256: a.SHA256, Findings: []ScanFinding{}}
	add := func(code, severity, message, detail string) {
		report.Findings = append(report.Findings, ScanFinding{Code: code, Severity: severity, Message: message, Detail: detail})
	}
	if report.SizeBytes > cfg.MaxBytes {
		add(FindingSizeExceeded, SeverityError, "The uploaded module is larger than the configured ceiling.",
			fmt.Sprintf("%d bytes exceed %d bytes", report.SizeBytes, cfg.MaxBytes))
	}
	switch {
	case len(a.Data) == 0:
		report.Kind = "empty"
		add(FindingEmptyModule, SeverityWarning, "The uploaded module has no bytes.", "")
	case looksLikeWASM(a.Data):
		report.Kind = "wasm-module"
		module, err := parseWASM(a.Data)
		if err != nil {
			add(FindingWasmMalformed, SeverityError, "The WASM binary could not be parsed.", err.Error())
		} else {
			report.Imports = module.imports
			for _, imp := range module.imports {
				if !importAllowed(cfg.AllowedImports, imp) {
					add(FindingImportNotAllowed, SeverityError, "The module imports a host function that is not on the allow-list.", imp)
				}
			}
			if module.shared && !cfg.AllowSharedMemory {
				add(FindingSharedMemory, SeverityError, "Shared WebAssembly.Memory is not allowed (R10.0).", "")
			}
			if module.maxPages > cfg.MaxWASMPages {
				add(FindingWasmMemoryLimit, SeverityError, "The WASM memory limit exceeds the configured ceiling.", fmt.Sprintf("%d pages exceed %d", module.maxPages, cfg.MaxWASMPages))
			}
			if module.start {
				add("wasm-start", SeverityWarning, "The WASM module declares a start function.", "")
			}
		}
	default:
		switch strings.ToLower(strings.TrimSpace(a.ContentType)) {
		case "application/wasm":
			report.Kind = "wasm-module"
			add(FindingWasmMagic, SeverityError, "The artifact declares application/wasm but does not carry the WASM magic bytes.", "")
		case "application/javascript", "text/javascript", "application/ecmascript":
			report.Kind = "javascript"
			if int64(len(a.Data)) > cfg.MaxJSBytes {
				add(FindingSizeExceeded, SeverityError, "The JavaScript artifact exceeds its kind limit.", "")
			}
			for _, f := range scanJavaScript(a.Data) {
				report.Findings = append(report.Findings, f)
			}
		case "application/zip":
			report.Kind = "zip"
			if int64(len(a.Data)) > cfg.MaxZipUncompressed {
				add(FindingSizeExceeded, SeverityError, "The ZIP artifact exceeds its kind limit.", "")
			}
			report.Findings = append(report.Findings, e.scanZip(cfg, a.Data)...)
		case "application/json":
			report.Kind = "json"
			if int64(len(a.Data)) > cfg.MaxJSONBytes {
				add(FindingSizeExceeded, SeverityError, "The JSON artifact exceeds its kind limit.", "")
			}
		default:
			report.Kind = "data"
		}
	}
	if report.Kind == "wasm-module" && int64(len(a.Data)) > cfg.MaxWASMBytes {
		add(FindingSizeExceeded, SeverityError, "The WASM artifact exceeds its kind limit.", "")
	}
	report.OK = true
	for _, f := range report.Findings {
		if f.Severity == SeverityError {
			report.OK = false
		}
	}
	return report
}

// scanErrorCodes lists the codes of the error findings of a report.
func scanErrorCodes(report ScanReport) []string {
	codes := []string{}
	for _, f := range report.Findings {
		if f.Severity == SeverityError {
			codes = append(codes, f.Code)
		}
	}
	return codes
}

// applyUploadScan runs the scan over a completed artifact and sets its review state.
// An artifact that an admin already approved keeps its approval when a later scan
// passes; a failed scan always rejects it. The returned error rejects the upload.
func (e *Engine) applyUploadScan(a *Artifact) error {
	cfg := e.UploadScanConfig()
	approved := a.ReviewState == ReviewApproved
	if !cfg.Enabled {
		if a.ReviewState == "" {
			a.ReviewState = ReviewNotRequired
		}
		a.ReviewQuorum = cfg.ReviewQuorum
		return nil
	}
	report := e.scanBytes(cfg, *a)
	report.ScannedAt = e.Now().UTC()
	a.Scan = &report
	a.ReviewQuorum = cfg.ReviewQuorum
	if !report.OK {
		a.ReviewState = ReviewRejected
		a.ReviewReason = "upload scan failed: " + strings.Join(scanErrorCodes(report), ", ")
		return fmt.Errorf("%w: %w: %s", ErrArtifactRejected, ErrInvalid, a.ReviewReason)
	}
	if approved {
		// An admin approval survives a later passing scan (R12.1: the quorum was
		// already reached).
		a.ReviewState = ReviewApproved
		return nil
	}
	if cfg.RequireReview {
		a.ReviewState = ReviewPending
		a.ReviewReason = "awaiting admin review"
		return nil
	}
	a.ReviewState = ReviewNotRequired
	a.ReviewReason = ""
	return nil
}

// moduleApproved reports whether an artifact may be used as a job module.
func (e *Engine) moduleApproved(a Artifact) error {
	switch a.ReviewState {
	case "", ReviewNotRequired, ReviewApproved:
		return nil
	case ReviewPending:
		return fmt.Errorf("%w: %w: module artifact %s awaits admin review", ErrArtifactPending, ErrConflict, a.ArtifactID)
	case ReviewRejected:
		return fmt.Errorf("%w: %w: module artifact %s was rejected by the upload scan", ErrArtifactRejected, ErrConflict, a.ArtifactID)
	}
	return fmt.Errorf("%w: module artifact %s has an unknown review state %q", ErrConflict, a.ArtifactID, a.ReviewState)
}

// RecordUploadOwner remembers the uploader of an artifact. Owner, admin and director
// code is auto-approved (R13.1); every other upload keeps the review state of the scan.
func (e *Engine) RecordUploadOwner(ctx context.Context, id, owner string, ownerCode bool) error {
	return e.Store.Update(ctx, func(tx store.Tx) error {
		var a Artifact
		if err := tx.Get(store.Artifacts, id, &a); err != nil {
			return err
		}
		if owner != "" {
			a.Owner = owner
		}
		if ownerCode && a.Complete {
			now := e.Now().UTC()
			a.ReviewedAt = &now
			if a.Scan != nil && !a.Scan.OK {
				a.ReviewState = ReviewRejected
				a.ReviewReason = "upload scan failed: " + strings.Join(scanErrorCodes(*a.Scan), ", ")
			} else {
				a.ReviewState = ReviewApproved
				a.ReviewReason = "owner code auto-approved (R13.1)"
			}
		}
		return tx.Put(store.Artifacts, id, a)
	})
}

// ReviewArtifact records one admin review decision of an artifact. Approvals are
// counted per distinct reviewer; the artifact is approved when the quorum of R12.1
// is reached. A rejection is final until a later approval overrides it.
func (e *Engine) ReviewArtifact(ctx context.Context, id, reviewer, decision, reason string) (Artifact, error) {
	var a Artifact
	cfg := e.UploadScanConfig()
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if err := tx.Get(store.Artifacts, id, &a); err != nil {
			return err
		}
		now := e.Now().UTC()
		switch decision {
		case ReviewDecisionApprove:
			if a.ReviewState == ReviewRejected && a.Scan != nil && !a.Scan.OK {
				return fmt.Errorf("%w: artifact %s was rejected by the upload scan", ErrArtifactRejected, a.ArtifactID)
			}
			if a.ReviewState != ReviewApproved {
				found := false
				for _, r := range a.Reviewers {
					if r == reviewer {
						found = true
						break
					}
				}
				if !found {
					a.Reviewers = append(a.Reviewers, reviewer)
				}
				if len(a.Reviewers) >= cfg.ReviewQuorum {
					a.ReviewState = ReviewApproved
				} else {
					a.ReviewState = ReviewPending
				}
			}
			a.ReviewReason = reason
			a.ReviewedAt = &now
		case ReviewDecisionReject:
			a.ReviewState = ReviewRejected
			a.ReviewReason = reason
			a.ReviewedAt = &now
		default:
			return fmt.Errorf("%w: unknown review decision %q", ErrInvalid, decision)
		}
		if err := tx.Put(store.Artifacts, id, a); err != nil {
			return err
		}
		return e.auditReview(tx, a, reviewer, decision, reason)
	})
	if err != nil {
		return Artifact{}, err
	}
	return a, nil
}

// auditReview writes the security log row of one review decision (R25.1).
func (e *Engine) auditReview(tx store.Tx, a Artifact, reviewer, decision, reason string) error {
	row := struct {
		ArtifactID  string    `json:"artifactId"`
		Reviewer    string    `json:"reviewer"`
		Decision    string    `json:"decision"`
		Reason      string    `json:"reason"`
		ReviewState string    `json:"reviewState"`
		At          time.Time `json:"at"`
	}{a.ArtifactID, reviewer, decision, reason, a.ReviewState, e.Now().UTC()}
	return tx.Put(store.Audit, auth.ID(), row)
}

// ReviewQueue lists the artifacts of one review state, newest first by id.
func (e *Engine) ReviewQueue(ctx context.Context, state string, limit int) ([]Artifact, error) {
	if state == "" {
		state = ReviewPending
	}
	switch state {
	case ReviewNotRequired, ReviewPending, ReviewApproved, ReviewRejected:
	default:
		return nil, fmt.Errorf("%w: unknown review state %q", ErrInvalid, state)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	out := []Artifact{}
	err := e.Store.View(ctx, func(r store.Reader) error {
		rows, err := r.List(store.Artifacts)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var a Artifact
			if err = json.Unmarshal(row.Value, &a); err != nil {
				return err
			}
			if a.ReviewState != state {
				continue
			}
			a.Data = nil
			out = append(out, a)
			if len(out) >= limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RescanArtifact runs the upload scan again over a stored artifact and updates its
// review state. An approved artifact stays approved; a failed scan rejects it.
func (e *Engine) RescanArtifact(ctx context.Context, id string) (Artifact, error) {
	var a Artifact
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if err := tx.Get(store.Artifacts, id, &a); err != nil {
			return err
		}
		if !a.Complete {
			return fmt.Errorf("%w: artifact %s is not complete", ErrConflict, id)
		}
		// The scan error is deliberately ignored here: the report and the new
		// review state are the result of the rescan, not a failure of the call.
		_ = e.applyUploadScan(&a)
		return tx.Put(store.Artifacts, id, a)
	})
	if err != nil {
		return Artifact{}, err
	}
	a.Data = nil
	return a, nil
}
