// Path: cmd/buildrunner/main.go | Author: CommandCode for the owner | 2026-11-21
// What/why: Go port of web/pages/js/build-runner-html.mjs (owner decision D-W10-12).
// The server is Go and JavaScript runs only in browsers; Node stays dev-only for
// node --test. Same inputs and output: web/pages/js/runner.js, plus the two modules
// whose functions its inline copy needs, -> web/pages/runner.html with the D-W10-5
// strict CSP over the EXACT inline script body, the D-W10-9 runner pin
// data-runner-pin="<sha256 of that same body>:<first operator key id>" and the
// runner build id.
//
// Usage: go run ./cmd/buildrunner [-root dir] [-check]
//
//	-root   pools-core directory (default: cwd)
//	-check  exit 1 and name the part that differs when runner.html is not what the
//	        build would write; never writes the file.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	runnerRel     = "web/pages/js/runner.js"
	gpuPickerRel  = "web/pages/js/gpu_picker_T83.js"
	hashRel       = "web/pages/js/platform/hash.js"
	runnerHTMLRel = "web/pages/runner.html"

	// The two import lines the inline copy replaces with the function sources.
	gpuPickerImport = "import { createGPUPicker, createLanePool } from './gpu_picker_T83.js';"
	hashImport      = "import { sha256Hex, sha256Pure, toBytes } from './platform/hash.js';"

	// The marker that opens the generated inline block (runner.js RUNNER_INLINE_BEGIN).
	runnerInlineBegin = "/* INLINED FROM js/runner.js - generated, do not edit */"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without os.Exit, so tests exercise the real flags and exit codes.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("buildrunner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rootFlag := fs.String("root", ".", "pools-core directory (default: cwd)")
	checkFlag := fs.Bool("check", false, "exit 1 when runner.html is out of date, without writing it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		fmt.Fprintf(stderr, "buildrunner: root %q: %v\n", *rootFlag, err)
		return 1
	}

	html, err := Build(root)
	if err != nil {
		fmt.Fprintf(stderr, "buildrunner: %v\n", err)
		return 1
	}

	outPath := filepath.Join(root, filepath.FromSlash(runnerHTMLRel))
	if *checkFlag {
		return checkOutput(root, outPath, html, stdout, stderr)
	}
	if err := os.WriteFile(outPath, html, 0o644); err != nil {
		fmt.Fprintf(stderr, "buildrunner: write %s: %v\n", outPath, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s\n", outPath)
	return 0
}

// Build renders web/pages/runner.html from the JavaScript sources under root.
func Build(root string) ([]byte, error) {
	runnerSource, err := readJS(filepath.Join(root, filepath.FromSlash(runnerRel)))
	if err != nil {
		return nil, err
	}
	gpuSource, err := readJS(filepath.Join(root, filepath.FromSlash(gpuPickerRel)))
	if err != nil {
		return nil, err
	}
	hashSource, err := readJS(filepath.Join(root, filepath.FromSlash(hashRel)))
	if err != nil {
		return nil, err
	}

	buildID, err := runnerBuildID(runnerSource)
	if err != nil {
		return nil, err
	}
	gpuPicker, err := functionSource(gpuSource, "createGPUPicker")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", gpuPickerRel, err)
	}
	lanePool, err := functionSource(gpuSource, "createLanePool")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", gpuPickerRel, err)
	}
	toBytes, err := functionSource(hashSource, "toBytes")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", hashRel, err)
	}
	sha256Pure, err := functionSource(hashSource, "sha256Pure")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", hashRel, err)
	}
	sha256Hex, err := functionSource(hashSource, "sha256Hex")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", hashRel, err)
	}

	firstKey, err := firstOperatorKeyID(runnerSource)
	if err != nil {
		return nil, err
	}

	inline := buildInlineRunnerScript(runnerSource,
		gpuPicker+"\n"+lanePool,
		toBytes+"\n"+sha256Pure+"\n"+sha256Hex)

	// R10-FIX row 1: the text the browser hashes is the WHOLE text between
	// <script type="module"> and </script>, including the template padding. The
	// same string feeds the CSP hash, the pin and the emitted element, so both
	// always cover exactly what the browser sees.
	scriptBody := "\n" + inline + "  "

	sum := sha256.Sum256([]byte(scriptBody))
	csp := "default-src 'none'; script-src 'sha256-" +
		base64.StdEncoding.EncodeToString(sum[:]) +
		"' blob:; worker-src blob:; connect-src 'none'"

	// R10-FIX owner D-W10-9 ("Runner self-reports"): the page bakes the identity
	// of the runner it ships as data-runner-pin="<sha256>:<keyId>". ONE pin: the
	// hash is over the exact script element text, the same bytes the CSP hash
	// covers, and the key id is the first operator key of the pinned ring.
	runnerPin := hex.EncodeToString(sum[:]) + ":" + firstKey

	return []byte(fmt.Sprintf(runnerHTMLTemplate, runnerPin, csp, inline, buildID)), nil
}

// operatorKeysOpen opens the pinned operator key ring in runner.js. The key ids
// are the top-level property names of the frozen object that follows.
const operatorKeysOpen = "export const OPERATOR_KEYS = Object.freeze({"

// firstOperatorKeyID reproduces the reference's Object.keys(OPERATOR_KEYS)[0]
// with an empty-string fallback: the first key id of the ring in source order
// (the ids are not array indices, so JS keeps insertion order).
func firstOperatorKeyID(source string) (string, error) {
	at := strings.Index(source, operatorKeysOpen)
	if at < 0 {
		return "", errors.New("runner.js: no OPERATOR_KEYS ring")
	}
	open := at + len(operatorKeysOpen) - 1
	close := skipBalanced(source, open, '{', '}')
	if close < 0 {
		return "", errors.New("runner.js: unbalanced OPERATOR_KEYS ring")
	}
	depth := 1
	for i := open + 1; i < close; {
		c := source[i]
		switch {
		case c == '\'' || c == '"':
			end := skipString(source, i)
			if depth == 1 {
				j := end
				for j < close && isJSWhitespaceByte(source[j]) {
					j++
				}
				if j < close && source[j] == ':' {
					return source[i+1 : end-1], nil
				}
			}
			i = end
		case c == '`':
			i = skipTemplate(source, i)
		case c == '/' && i+1 < close && source[i+1] == '/':
			i = skipLineComment(source, i)
		case c == '/' && i+1 < close && source[i+1] == '*':
			i = skipBlockComment(source, i)
		case c == '{':
			depth++
			i++
		case c == '}':
			depth--
			i++
		default:
			i++
		}
	}
	return "", nil
}

// buildInlineRunnerScript is the Go port of runner.js buildInlineRunnerScript:
// replace the two imports with the function sources, strip the export keywords,
// trim the trailing whitespace, and wrap the result in the inline marker. The
// reference normalizes CRLF at this point (T91), the files here are LF.
func buildInlineRunnerScript(source, gpuFunctions, hashFunctions string) string {
	text := normalizeLF(source)
	text = replaceWholeLine(text, gpuPickerImport, gpuFunctions)
	text = replaceWholeLine(text, hashImport, hashFunctions)
	text = stripExportPrefixes(text)
	text = exportBlockRE.ReplaceAllString(text, "")
	text = trailingJSSpaceRE.ReplaceAllString(text, "")
	return runnerInlineBegin + "\n" + text + "\n"
}

func readJS(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return normalizeLF(string(data)), nil
}

// normalizeLF matches the reference's final stripped.replace(/\r\n?/g, '\n').
func normalizeLF(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// replaceWholeLine replaces the first line that equals want, like the reference's
// anchored /^...$/m replace. A missing line is not an error, exactly like JS.
func replaceWholeLine(text, want, repl string) string {
	start := 0
	for {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			if text[start:] == want {
				return text[:start] + repl
			}
			return text
		}
		end += start
		if text[start:end] == want {
			return text[:start] + repl + text[end:]
		}
		start = end + 1
	}
}

// stripExportPrefixes removes the export keyword before a declaration, the port of
// /^export\s+(?=(?:async\s+function|function|const|let|var|class)\b)/gm.
func stripExportPrefixes(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	i := 0
	for i < len(text) {
		if (i == 0 || text[i-1] == '\n') && strings.HasPrefix(text[i:], "export") {
			k := i + len("export")
			wsStart := k
			for k < len(text) {
				r, size := utf8.DecodeRuneInString(text[k:])
				if !isJSSpace(r) {
					break
				}
				k += size
			}
			if k > wsStart && exportKeywordAt(text[k:]) {
				i = k
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

func exportKeywordAt(s string) bool {
	for _, kw := range []string{"function", "const", "let", "var", "class"} {
		if strings.HasPrefix(s, kw) && wordBoundaryAt(s, len(kw)) {
			return true
		}
	}
	if strings.HasPrefix(s, "async") {
		j := len("async")
		wsStart := j
		for j < len(s) {
			r, size := utf8.DecodeRuneInString(s[j:])
			if !isJSSpace(r) {
				break
			}
			j += size
		}
		if j > wsStart && strings.HasPrefix(s[j:], "function") && wordBoundaryAt(s[j:], len("function")) {
			return true
		}
	}
	return false
}

func wordBoundaryAt(s string, n int) bool {
	return n >= len(s) || !isWordByte(s[n])
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029',
		'\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func isJSWhitespaceByte(c byte) bool {
	switch c {
	case '\t', '\n', '\v', '\f', '\r', ' ':
		return true
	}
	return false
}

const jsSpaceClass = `[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

var (
	exportBlockRE     = regexp.MustCompile(`(?m)^export` + jsSpaceClass + `*\{[^}]*\}` + jsSpaceClass + `*;?` + jsSpaceClass + `*$`)
	trailingJSSpaceRE = regexp.MustCompile(jsSpaceClass + `+$`)
	runnerBuildIDRE   = regexp.MustCompile(`(?m)^export const RUNNER_BUILD_ID = '([^'\\\r\n]*)';[ \t]*$`)
)

func runnerBuildID(source string) (string, error) {
	m := runnerBuildIDRE.FindStringSubmatch(source)
	if m == nil {
		return "", errors.New("runner.js: no RUNNER_BUILD_ID declaration")
	}
	return m[1], nil
}

// functionSource returns the source text of a top-level function declaration the
// way Function.prototype.toString does: from the function keyword (or async) to
// the closing brace of the body, comments and spacing included.
func functionSource(source, name string) (string, error) {
	needle := "function " + name + "("
	from := 0
	for {
		k := strings.Index(source[from:], needle)
		if k < 0 {
			return "", fmt.Errorf("function %s not found", name)
		}
		k += from
		from = k + len(needle)

		start := k
		if k >= len("async ") && source[k-len("async "):k] == "async " {
			start = k - len("async ")
		}
		lineStart := strings.LastIndexByte(source[:start], '\n') + 1
		prefix := source[lineStart:start]
		if prefix != "" && prefix != "export " {
			continue
		}

		open := k + len("function "+name)
		if open >= len(source) || source[open] != '(' {
			continue
		}
		close := skipBalanced(source, open, '(', ')')
		if close < 0 {
			return "", fmt.Errorf("function %s: unbalanced parameter list", name)
		}
		j := close + 1
		for j < len(source) && isJSWhitespaceByte(source[j]) {
			j++
		}
		if j >= len(source) || source[j] != '{' {
			return "", fmt.Errorf("function %s: no body", name)
		}
		end := skipBalanced(source, j, '{', '}')
		if end < 0 {
			return "", fmt.Errorf("function %s: unbalanced body", name)
		}
		return source[start : end+1], nil
	}
}

// skipBalanced returns the index of the close byte that matches src[open]. It
// skips strings, template literals (including ${} expressions), comments and
// regular expression literals so braces inside them do not count.
func skipBalanced(src string, open int, openCh, closeCh byte) int {
	depth := 0
	for i := open; i < len(src); {
		c := src[i]
		switch {
		case c == '\'' || c == '"':
			i = skipString(src, i)
		case c == '`':
			i = skipTemplate(src, i)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			i = skipLineComment(src, i)
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = skipBlockComment(src, i)
		case c == '/' && regexAllowedBefore(src, i):
			i = skipRegex(src, i)
		case c == openCh:
			depth++
			i++
		case c == closeCh:
			depth--
			i++
			if depth == 0 {
				return i - 1
			}
		default:
			i++
		}
	}
	return -1
}

func skipString(src string, i int) int {
	quote := src[i]
	i++
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
		case quote:
			return i + 1
		default:
			i++
		}
	}
	return i
}

func skipTemplate(src string, i int) int {
	i++
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
		case '`':
			return i + 1
		case '$':
			if i+1 < len(src) && src[i+1] == '{' {
				i = skipTemplateExpression(src, i+2)
			} else {
				i++
			}
		default:
			i++
		}
	}
	return i
}

func skipTemplateExpression(src string, i int) int {
	depth := 1
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\'' || c == '"':
			i = skipString(src, i)
		case c == '`':
			i = skipTemplate(src, i)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			i = skipLineComment(src, i)
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = skipBlockComment(src, i)
		case c == '/' && regexAllowedBefore(src, i):
			i = skipRegex(src, i)
		case c == '{':
			depth++
			i++
		case c == '}':
			depth--
			i++
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return i
}

func skipLineComment(src string, i int) int {
	if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(src)
}

func skipBlockComment(src string, i int) int {
	if j := strings.Index(src[i+2:], "*/"); j >= 0 {
		return i + 2 + j + 2
	}
	return len(src)
}

func skipRegex(src string, i int) int {
	i++
	inClass := false
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
		case '[':
			inClass = true
			i++
		case ']':
			inClass = false
			i++
		case '/':
			if inClass {
				i++
				continue
			}
			i++
			for i < len(src) && isWordByte(src[i]) {
				i++
			}
			return i
		case '\n':
			return i
		default:
			i++
		}
	}
	return i
}

// regexAllowedBefore uses the usual lexical heuristic: after a value or a closing
// bracket a slash divides; otherwise it opens a regular expression.
func regexAllowedBefore(src string, i int) bool {
	j := i - 1
	for j >= 0 && isJSWhitespaceByte(src[j]) {
		j--
	}
	if j < 0 {
		return true
	}
	c := src[j]
	return !(isWordByte(c) || c == ')' || c == ']')
}

const runnerHTMLTemplate = `<!DOCTYPE html>
<!-- WO_T3_CONTRIBUTE_PAGE runner.html - Sandbox Runner (ccgsc-run origin) -->
<!-- Author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17 -->
<!-- Truth: WO_T3_CONTRIBUTE_PAGE.md, GRILL_WWPP_R1.md (R29.1, R37.1, R39.1), SPLIT_DESIGN_W10.md section 8 (D-W10-5) -->
<!-- GENERATED FILE: the script below is generated from js/runner.js by -->
<!-- web/pages/js/build-runner-html.mjs. Do not edit the inline script by hand. -->
<html lang="en" data-runner-pin="%s">
<head>
  <meta charset="UTF-8">
  <!-- D-W10-5 strict sandbox CSP: no egress, no inline script beyond the pinned -->
  <!-- runner hash, Workers only from verified blob: URLs. -->
  <meta http-equiv="Content-Security-Policy" content="%s">
  <title>CCGSC Sandbox Runner</title>
  <!-- owner D-W10-9: the runner's own pin, read back by the runner off the
       document element and reported in its ready message. A page attribute keeps
       the CSP untouched (no second inline script) and the hash non-circular. -->
</head>
<body>
  <!-- Opaque execution sandbox. All communication strictly over transferred MessagePort -->
  <!-- The runner script is inlined from js/runner.js: the sandbox has an opaque origin -->
  <!-- and a module fetch of js/runner.js would be blocked by CORS. -->
  <script type="module">
%s  </script>
</body>
</html>
<!-- END OF runner.html -->
<!-- build id: %s -->
`

// checkOutput implements -check: a fresh file passes, any difference exits 1 and
// names the part that differs.
func checkOutput(root, outPath string, expected []byte, stdout, stderr io.Writer) int {
	actual, err := os.ReadFile(outPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "runner.html is out of date: %s is missing\n", outPath)
		fmt.Fprintf(stderr, "run: go run ./cmd/buildrunner -root \"%s\"\n", root)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "buildrunner: read %s: %v\n", outPath, err)
		return 1
	}
	if bytes.Equal(actual, expected) {
		fmt.Fprintln(stdout, "runner.html is up to date")
		return 0
	}
	part, line := firstDifference(string(expected), string(actual))
	fmt.Fprintf(stderr, "runner.html is out of date: %s differs at line %d\n", part, line)
	fmt.Fprintf(stderr, "run: go run ./cmd/buildrunner -root \"%s\"\n", root)
	return 1
}

type htmlPart struct {
	text  string
	start int
	ok    bool
}

func extractPart(s, prefix, suffix string) htmlPart {
	i := strings.Index(s, prefix)
	if i < 0 {
		return htmlPart{}
	}
	start := i + len(prefix)
	j := strings.Index(s[start:], suffix)
	if j < 0 {
		return htmlPart{}
	}
	return htmlPart{text: s[start : start+j], start: start, ok: true}
}

func extractInline(s string) htmlPart {
	return extractPart(s, `<script type="module">`+"\n", "  </script>")
}

func extractCSP(s string) htmlPart {
	return extractPart(s, `<meta http-equiv="Content-Security-Policy" content="`, `">`)
}

func extractPin(s string) htmlPart {
	return extractPart(s, `<html lang="en" data-runner-pin="`, `">`)
}

func extractBuildID(s string) htmlPart {
	return extractPart(s, `<!-- build id: `, " -->")
}

// firstDifference names the first part of the page that is not what the build
// would write: the inline script, the CSP hash, the runner pin, the build id, or
// other bytes.
func firstDifference(expected, actual string) (string, int) {
	eInline := extractInline(expected)
	aInline := extractInline(actual)
	if eInline.ok && aInline.ok && eInline.text != aInline.text {
		return "inline script", lineAt(actual, aInline.start+commonPrefixLen(eInline.text, aInline.text))
	}
	eCSP := extractCSP(expected)
	aCSP := extractCSP(actual)
	if eCSP.ok && aCSP.ok && eCSP.text != aCSP.text {
		return "CSP hash", lineAt(actual, aCSP.start+commonPrefixLen(eCSP.text, aCSP.text))
	}
	ePin := extractPin(expected)
	aPin := extractPin(actual)
	if ePin.ok && aPin.ok && ePin.text != aPin.text {
		return "runner pin", lineAt(actual, aPin.start+commonPrefixLen(ePin.text, aPin.text))
	}
	eBuild := extractBuildID(expected)
	aBuild := extractBuildID(actual)
	if eBuild.ok && aBuild.ok && eBuild.text != aBuild.text {
		return "build id", lineAt(actual, aBuild.start+commonPrefixLen(eBuild.text, aBuild.text))
	}
	return "content", lineAt(actual, commonPrefixLen(expected, actual))
}

func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func lineAt(s string, off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(s) {
		off = len(s)
	}
	return 1 + strings.Count(s[:off], "\n")
}
