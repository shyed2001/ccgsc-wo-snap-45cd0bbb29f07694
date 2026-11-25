"""
File: F:\\tmp\\wwpp\\spike_sandbox2\\serve.py
Author: hermes MiniMax-M3 for Claude Opus 5.5. Created 2026-11-17 (Dhaka).
What/why: serve the WO-SPIKE2 sandbox on two ports = two origins (demo parent 8711, run iframe 8712),
  plus a /report endpoint that stores the CHECK lines the page posts, so any browser (Firefox, Android
  Chrome) can run the spike and the result is readable without DevTools.
Run: python serve.py   (Ctrl+C stops both). Bound to 127.0.0.1 only unless SPIKE_HOST is set.
"""
import functools
import http.server
import json
import os
import threading

HERE = os.path.dirname(os.path.abspath(__file__))
Handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=HERE)

# SPIKE_HOST=<tailnet IP> lets a phone on the tailnet open the spike.
HOST = os.environ.get("SPIKE_HOST", "127.0.0.1")
PARENT_PORT = int(os.environ.get("SPIKE_PARENT_PORT", "8711"))
RUN_PORT = int(os.environ.get("SPIKE_RUN_PORT", "8712"))

REPORT_LOCK = threading.Lock()
REPORTS = []


class SpikeHandler(http.server.SimpleHTTPRequestHandler):
    """Static files plus POST /report -> stores the CHECK lines and prints them to stdout."""

    def do_POST(self):  # noqa: N802 (stdlib naming)
        if not self.path.startswith("/report"):
            self.send_error(404, "unknown endpoint")
            return
        try:
            length = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(length).decode("utf-8", "replace")
        except Exception as exc:  # pragma: no cover - defensive
            self.send_error(400, str(exc))
            return
        with REPORT_LOCK:
            REPORTS.append(body)
            index = len(REPORTS)
        print("\n=== REPORT #%d ===" % index, flush=True)
        print(body, flush=True)
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, fmt, *args):
        sys_out = "[spike2] " + (fmt % args)
        print(sys_out, flush=True)


servers = [http.server.ThreadingHTTPServer((HOST, p), SpikeHandler) for p in (PARENT_PORT, RUN_PORT)]
for s in servers[1:]:
    threading.Thread(target=s.serve_forever, daemon=True).start()
print("parent: http://127.0.0.1:%d/parent.html   run origin: http://127.0.0.1:%d"
      % (PARENT_PORT, RUN_PORT), flush=True)
try:
    servers[0].serve_forever()
except KeyboardInterrupt:
    pass
finally:
    for s in servers:
        s.shutdown()
# END OF FILE serve.py
