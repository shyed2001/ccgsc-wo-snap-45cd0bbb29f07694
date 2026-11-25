#!/usr/bin/env bash
# Path: scripts/pools-core.sh | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
# What/why: Linux/bash counterpart of scripts/pools-core.ps1 for pools-core (WO-T5, WWPP-R41.1).
#   Same actions (start|stop|restart|status|logs|tail|config|store|diagnostics|test), same
#   logs/launcher.jsonl audit trail, no interactive menu and no global install.
# WO-T2.2 (hermes MiniMax-M3, 2026-11-17): the monitor link now opens the real
#   /pools/monitor.html page served by pools-core, not the old /web/ placeholder.
set -uo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT" || exit 1

BINARY="$PROJECT_ROOT/bin/pools-core"
DATA_DIR="$PROJECT_ROOT/data"
LOG_DIR="$PROJECT_ROOT/logs"
PID_FILE="$DATA_DIR/pools-core.pid"
LAUNCHER_ENV="$DATA_DIR/launcher.env.json"
LAUNCHER_LOG="$LOG_DIR/launcher.jsonl"
CONFIG_FILE="$PROJECT_ROOT/config.example.json"
DEFAULT_ADDR="127.0.0.1:8091"

# Go toolchain cache locations: respect the caller's environment, never install anything.
[[ -n "${GOMODCACHE:-}" ]] && export GOMODCACHE
[[ -n "${GOCACHE:-}" ]] && export GOCACHE
[[ -n "${GOTOOLCHAIN:-}" ]] && export GOTOOLCHAIN

# ------------------------------------------------------------------ helpers ----
now_utc() { date -u +"%Y-%m-%dT%H:%M:%S.000Z"; }
have() { command -v "$1" >/dev/null 2>&1; }
ensure_layout() {
  mkdir -p "$PROJECT_ROOT/bin" "$DATA_DIR" "$LOG_DIR"
  chmod 700 "$DATA_DIR" 2>/dev/null || true
}

# Append one JSON line per launcher action: ts (UTC), action, result, ms.
launcher_log() {
  local action="$1" result="$2" ms="$3" detail="${4:-}"
  ensure_layout
  local escaped
  escaped="$(printf '%s' "$detail" | sed 's/\\/\\\\/g; s/"/\\"/g')"
  if [[ -n "$detail" ]]; then
    printf '{"ts":"%s","action":"%s","result":"%s","ms":%s,"detail":"%s"}\n' \
      "$(now_utc)" "$action" "$result" "$ms" "$escaped" >> "$LAUNCHER_LOG" 2>/dev/null
  else
    printf '{"ts":"%s","action":"%s","result":"%s","ms":%s}\n' \
      "$(now_utc)" "$action" "$result" "$ms" >> "$LAUNCHER_LOG" 2>/dev/null
  fi
  return 0
}

# Read one flat string key out of data/launcher.env.json (no jq dependency).
env_get() {
  local key="$1" out=""
  if [[ -f "$LAUNCHER_ENV" ]]; then
    out="$(grep -o "\"$key\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" "$LAUNCHER_ENV" 2>/dev/null \
      | head -n1 | sed 's/.*:[[:space:]]*"\(.*\)"/\1/')"
  fi
  printf '%s' "$out"
  return 0
}

# Rewrite data/launcher.env.json with one updated key; the file stays a flat JSON object.
# Key order is fixed so the file is diff-friendly and always valid JSON.
env_set() {
  local key="$1" value="$2"
  ensure_layout
  local addr dbg lvl secret store
  addr="$(env_get POOLS_ADDR)"
  dbg="$(env_get POOLS_DEBUG)"
  lvl="$(env_get POOLS_LOG_LEVEL)"
  secret="$(env_get POOLS_JWT_SECRET)"
  store="$(env_get POOLS_STORE)"
  case "$key" in
    POOLS_ADDR)       addr="$value" ;;
    POOLS_DEBUG)      dbg="$value" ;;
    POOLS_LOG_LEVEL)  lvl="$value" ;;
    POOLS_JWT_SECRET) secret="$value" ;;
    POOLS_STORE)      store="$value" ;;
  esac
  {
    printf '{\n'
    printf '  "_meta": "data/launcher.env.json | hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17 | launcher environment; scripts/pools-core.sh start reads the POOLS_* keys",\n'
    printf '  "updatedAt": "%s",\n' "$(now_utc)"
    printf '  "POOLS_ADDR": "%s",\n' "${addr:-127.0.0.1:8091}"
    printf '  "POOLS_DEBUG": "%s",\n' "${dbg:-1}"
    printf '  "POOLS_LOG_LEVEL": "%s",\n' "${lvl:-debug}"
    [[ -n "$secret" ]] && printf '  "POOLS_JWT_SECRET": "%s",\n' "$secret"
    printf '  "POOLS_STORE": "%s"\n' "${store:-sqlite}"
    printf '}\n'
  } > "$LAUNCHER_ENV.tmp"
  mv -f "$LAUNCHER_ENV.tmp" "$LAUNCHER_ENV"
  # Row 2 note: on POSIX/Linux, restrict file permissions to current user only (chmod 600)
  # complementing Windows DPAPI + FileSecurity ACLs on PowerShell.
  chmod 600 "$LAUNCHER_ENV" 2>/dev/null || true
  return 0
}

effective_address() {
  if [[ -n "${POOLS_ADDR:-}" ]]; then printf '%s' "$POOLS_ADDR"; return 0; fi
  local v; v="$(env_get POOLS_ADDR)"
  if [[ -n "$v" ]]; then printf '%s' "$v"; return 0; fi
  if [[ -f "$CONFIG_FILE" ]]; then
    v="$(grep -o '"addr"[[:space:]]*:[[:space:]]*"[^"]*"' "$CONFIG_FILE" 2>/dev/null \
      | head -n1 | sed 's/.*:[[:space:]]*"\(.*\)"/\1/')"
    if [[ -n "$v" ]]; then printf '%s' "$v"; return 0; fi
  fi
  printf '%s' "$DEFAULT_ADDR"
  return 0
}

current_store() {
  local v; v="$(env_get POOLS_STORE)"
  if [[ -n "$v" ]]; then printf '%s' "$v"; else printf 'sqlite'; fi
  return 0
}

current_mode() {
  local lvl dbg
  lvl="$(env_get POOLS_LOG_LEVEL)"; dbg="$(env_get POOLS_DEBUG)"
  if [[ -z "$lvl" && -z "$dbg" ]]; then printf 'debug'; return 0; fi
  if [[ "$lvl" == "debug" || "$dbg" == "1" || "$dbg" == "true" ]]; then printf 'debug'; else printf 'normal'; fi
  return 0
}

# Store backend -> Go driver. Only sqlite is implemented; postgres is an explicit stub.
store_driver() {
  case "$1" in
    sqlite)          printf 'sqlite' ;;
    postgres)        printf 'pg' ;;
    json|csv|redis)  printf '' ;;
    *)               printf '' ;;
  esac
  return 0
}
store_status() {
  case "$1" in
    sqlite)          printf 'implemented' ;;
    postgres)        printf 'stub' ;;
    json|csv|redis)  printf 'missing' ;;
    *)               printf 'unknown' ;;
  esac
  return 0
}
store_note() {
  case "$1" in
    sqlite)   printf 'modernc SQLite file (default data/pools.db)' ;;
    postgres) printf 'pgx seam only; store.Open returns not-implemented' ;;
    json)     printf 'no JSON store adapter in this build' ;;
    csv)      printf 'no CSV store adapter in this build' ;;
    redis)    printf 'no Redis store adapter in this build' ;;
    *)        printf 'unknown backend' ;;
  esac
  return 0
}

# Read PID record (JSON or raw integer) from PID file.
read_pid_record() {
  [[ -f "$PID_FILE" ]] || return 0
  local content; content="$(cat "$PID_FILE" 2>/dev/null)"
  [[ -n "$content" ]] || return 0
  if [[ "$content" =~ \"pid\"[[:space:]]*:[[:space:]]*([0-9]+) ]]; then
    local r_pid="${BASH_REMATCH[1]}"
    local r_path=""
    if [[ "$content" =~ \"path\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]]; then
      r_path="${BASH_REMATCH[1]}"
    fi
    local r_start=""
    if [[ "$content" =~ \"startTime\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]]; then
      r_start="${BASH_REMATCH[1]}"
    fi
    printf '%s\t%s\t%s' "$r_pid" "$r_path" "$r_start"
  else
    local num; num="$(tr -dc '0-9' <<< "$content")"
    [[ -n "$num" && "$num" -gt 0 ]] && printf '%s\t\t' "$num"
  fi
  return 0
}

# PID of the running service, verified against binary path and start time; empty when not running.
core_pid() {
  local rec; rec="$(read_pid_record)"
  [[ -n "$rec" ]] || return 0
  local pid rec_path rec_start
  IFS=$'\t' read -r pid rec_path rec_start <<< "$rec"
  [[ -n "$pid" && "$pid" -gt 0 ]] || return 0
  kill -0 "$pid" 2>/dev/null || return 0
  local exe want
  exe="$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)"
  want="$(readlink -f "${rec_path:-$BINARY}" 2>/dev/null || true)"
  if [[ -n "$exe" && -n "$want" && "$exe" != "$want" ]]; then return 0; fi
  # Row 11: require path verification; do not match by name alone
  if [[ -z "$exe" && -f "/proc/$pid/cmdline" ]]; then return 0; fi
  # Row 3 & 12: verify StartTime if recorded
  if [[ -n "$rec_start" ]]; then
    local cur_start=""
    cur_start="$(ps -o lstart= -p "$pid" 2>/dev/null | tr -d '\n' || true)"
    if [[ -n "$cur_start" && "$cur_start" != "$rec_start" ]]; then return 0; fi
  fi
  printf '%s' "$pid"
  return 0
}

latest_service_log() {
  # Row 6: sort by newest mtime, not by name
  ls -1t "$LOG_DIR"/pools-core-*.jsonl 2>/dev/null | head -n1
  return 0
}

health_json() {
  local addr="$1"
  if have curl; then
    curl -s --max-time 2 "http://$addr/healthz" 2>/dev/null
  fi
  return 0
}

# ------------------------------------------------------------------ actions ----
do_build() {
  ensure_layout
  echo "Building pools-core (go build -o bin/pools-core ./cmd/pools-core) ..."
  go build -o "$BINARY" ./cmd/pools-core || return 1
  return 0
}

do_start() {
  local mode="${1:-$(current_mode)}"
  local running; running="$(core_pid)"
  if [[ -n "$running" ]]; then echo "Already running (PID $running)."; return 0; fi
  ensure_layout
  if [[ ! -x "$BINARY" ]]; then do_build || return 1; fi

  local store status driver
  store="$(current_store)"; status="$(store_status "$store")"; driver="$(store_driver "$store")"
  case "$status" in
    missing) echo "Store backend '$store' is selected but not implemented by this Go build (implemented: sqlite; stub: postgres)." >&2; return 1 ;;
    stub)    echo "Warning: the PostgreSQL adapter is a skeleton; the service will exit with 'not implemented'." >&2 ;;
    unknown) echo "Unknown store backend '$store'." >&2; return 1 ;;
  esac
  export POOLS_DRIVER="$driver"

  # Row 1: ensure secret in both debug and normal modes
  if [[ -z "${POOLS_JWT_SECRET:-}" ]]; then
    local existing_secret; existing_secret="$(env_get POOLS_JWT_SECRET)"
    if [[ -n "$existing_secret" ]]; then
      export POOLS_JWT_SECRET="$existing_secret"
    else
      local secret; secret="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
      env_set POOLS_JWT_SECRET "$secret"
      export POOLS_JWT_SECRET="$secret"
      echo "Generated a local-only secret in data/launcher.env.json (value not shown)."
    fi
  fi

  if [[ "$mode" == "debug" ]]; then
    export POOLS_LOG_LEVEL=debug
    export POOLS_DEBUG=1
  else
    export POOLS_LOG_LEVEL=info
    export POOLS_DEBUG=0
  fi

  local addr; addr="$(effective_address)"
  # Row 8: warn/refuse non-loopback POOLS_ADDR unless ALLOW_REMOTE
  local host="${addr%:*}"
  if [[ "$addr" =~ ^:[0-9]+$ ]]; then host="127.0.0.1"; fi
  if [[ "$host" != "127.0.0.1" && "$host" != "localhost" && "$host" != "::1" && ! "$host" =~ ^127\. ]]; then
    if [[ "${ALLOW_REMOTE:-0}" != "1" && "${ALLOW_REMOTE:-false}" != "true" ]]; then
      echo "Refusing to start: POOLS_ADDR '$addr' is not a loopback address. Set ALLOW_REMOTE=1 or pass --allow-remote to permit non-loopback binding." >&2
      return 1
    else
      echo "Warning: binding non-loopback address '$addr' because ALLOW_REMOTE was set." >&2
    fi
  fi

  export POOLS_ADDR="$addr"
  local stamp; stamp="$(date +%Y%m%d-%H%M%S)"
  local args=()
  [[ -f "$CONFIG_FILE" ]] && args+=(-config "$CONFIG_FILE")
  nohup "$BINARY" "${args[@]}" >> "$LOG_DIR/launcher-$stamp.stdout.log" 2>> "$LOG_DIR/launcher-$stamp.stderr.log" &
  local pid=$!

  # Row 3: PID file records pid + path + StartTime
  local start_time=""
  start_time="$(ps -o lstart= -p "$pid" 2>/dev/null | tr -d '\n' || true)"
  printf '{"pid":%d,"path":"%s","startTime":"%s"}\n' "$pid" "$BINARY" "$start_time" > "$PID_FILE"

  local ready=0
  local i=0
  while [[ $i -lt 50 ]]; do
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "Service exited; see logs/launcher-$stamp.stderr.log" >&2
      printf '0' > "$PID_FILE"
      return 1
    fi
    local h_json; h_json="$(health_json "$addr")"
    if [[ -n "$h_json" ]]; then
      # Row 9: readiness bound to the started PID
      if ! kill -0 "$pid" 2>/dev/null; then
        echo "Service exited after health check; see logs/launcher-$stamp.stderr.log" >&2
        printf '0' > "$PID_FILE"
        return 1
      fi
      if [[ "$h_json" =~ \"(pid|PID)\"[[:space:]]*:[[:space:]]*([0-9]+) ]]; then
        local h_pid="${BASH_REMATCH[2]}"
        if [[ "$h_pid" -gt 0 && "$h_pid" -ne "$pid" ]]; then
          echo "Readiness check on $addr returned PID $h_pid, but started PID is $pid; port conflict." >&2
          kill -KILL "$pid" 2>/dev/null || true
          printf '0' > "$PID_FILE"
          return 1
        fi
      fi
      ready=1; break
    fi
    sleep 0.2
    i=$((i + 1))
  done
  if [[ "$ready" -ne 1 ]]; then
    echo "Health check timed out for $addr; inspect logs/launcher-$stamp.stderr.log" >&2
    # Row 10: cleanup on failed start
    kill -KILL "$pid" 2>/dev/null || true
    printf '0' > "$PID_FILE"
    return 1
  fi

  env_set POOLS_LOG_LEVEL "$POOLS_LOG_LEVEL"
  env_set POOLS_DEBUG "$POOLS_DEBUG"
  env_set POOLS_STORE "$store"
  env_set POOLS_ADDR "$addr"

  echo "Started pools-core (PID $pid)."
  echo "Mode      : $(printf '%s' "$mode" | tr '[:lower:]' '[:upper:]')  (POOLS_LOG_LEVEL=$POOLS_LOG_LEVEL, POOLS_DEBUG=$POOLS_DEBUG)"
  echo "Store     : $store  (driver=$driver)"
  echo "Listen    : http://$addr"
  echo "Monitor   : http://$addr/pools/monitor.html"
  echo "Logs      : logs/launcher-$stamp.stdout.log"
  return 0
}

do_stop() {
  local rec; rec="$(read_pid_record)"
  local pid; pid="$(core_pid)"
  if [[ -z "$pid" ]]; then
    echo "Not running."
    printf '0' > "$PID_FILE" 2>/dev/null
    return 0
  fi
  # Row 3: Verify pid, path, and StartTime before sending any signal
  if [[ -n "$rec" ]]; then
    local rec_pid rec_path rec_start
    IFS=$'\t' read -r rec_pid rec_path rec_start <<< "$rec"
    if [[ -n "$rec_pid" && "$pid" != "$rec_pid" ]]; then
      echo "PID mismatch before signal: expected $rec_pid, got $pid" >&2
      return 1
    fi
    if [[ -n "$rec_path" ]]; then
      local cur_exe exp_exe
      cur_exe="$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)"
      exp_exe="$(readlink -f "$rec_path" 2>/dev/null || true)"
      if [[ -n "$cur_exe" && -n "$exp_exe" && "$cur_exe" != "$exp_exe" ]]; then
        echo "Process path mismatch before signal: expected $exp_exe, got $cur_exe" >&2
        return 1
      fi
    fi
    if [[ -n "$rec_start" ]]; then
      local cur_start
      cur_start="$(ps -o lstart= -p "$pid" 2>/dev/null | tr -d '\n' || true)"
      if [[ -n "$cur_start" && "$cur_start" != "$rec_start" ]]; then
        echo "Process StartTime mismatch before signal: expected $rec_start, got $cur_start" >&2
        return 1
      fi
    fi
  fi
  # SIGTERM is the graceful path in cmd/pools-core (graceful shutdown on SIGINT/SIGTERM).
  kill -TERM "$pid" 2>/dev/null
  local i=0
  while [[ $i -lt 30 ]]; do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.1
    i=$((i + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null; fi
  printf '0' > "$PID_FILE"
  echo "Stopped the verified local process (PID $pid). SQLite transactions preserve committed data."
  return 0
}

do_status() {
  local pid addr port uptime health
  pid="$(core_pid)"
  addr="$(effective_address)"
  port="${addr##*:}"
  echo "--- pools-core status ---"
  if [[ -n "$pid" ]]; then
    uptime="$(ps -o etime= -p "$pid" 2>/dev/null | tr -d ' ')"
    [[ -z "$uptime" ]] && uptime="unknown"
    echo "  PID      : $pid (running)"
    echo "  Uptime   : $uptime"
  else
    echo "  PID      : not running"
    echo "  Uptime   : -"
  fi
  echo "  Address  : $addr   (port $port)"
  echo "  Mode     : $(current_mode | tr '[:lower:]' '[:upper:]')  (POOLS_LOG_LEVEL=$(env_get POOLS_LOG_LEVEL), POOLS_DEBUG=$(env_get POOLS_DEBUG))"
  echo "  Store    : $(current_store)"
  if [[ -n "$pid" ]]; then
    health="$(health_json "$addr")"
    if [[ -n "$health" ]]; then echo "  /healthz : $health"; else echo "  /healthz : unreachable"; fi
  else
    echo "  /healthz : not reachable (service not running)"
  fi
  return 0
}

redact_log_stream() {
  sed -E \
    -e 's/Bearer[[:space:]]+[A-Za-z0-9._-]+/Bearer <redacted>/gI' \
    -e 's/("(secret|jwt|token|password|credential|signing|key)"[[:space:]]*:[[:space:]]*)"[^"]*"/\1"<redacted>"/gI' \
    -e 's/\b(jwt([[:space:]]+secret)?|secret|token|password|credential|signing|key)[[:space:]]*(is|[:=])[[:space:]]*[^",[:space:]}]+/\1 \3 <redacted>/gI' \
    -e 's/\b[0-9a-fA-F]{32,64}\b/<redacted>/g'
}

do_logs() {
  local count="${1:-60}" log
  log="$(latest_service_log)"
  if [[ -z "$log" ]]; then echo "No service log yet (logs/pools-core-*.jsonl)."; return 0; fi
  echo "--- $(basename "$log") (last $count lines) ---"
  tail -n "$count" "$log" | redact_log_stream
  return 0
}

do_tail() {
  local filter="${1:-}" log
  log="$(latest_service_log)"
  if [[ -z "$log" ]]; then echo "No service log yet (logs/pools-core-*.jsonl)."; return 0; fi
  echo "--- live tail: $(basename "$log") ---"
  echo "    Ctrl+C stops the tail."
  if [[ -z "$filter" ]]; then
    tail -n 40 -f "$log" | redact_log_stream
  else
    tail -n 40 -f "$log" | grep --line-buffered -i "\"level\":\"$filter" | redact_log_stream
  fi
  return 0
}

do_config() {
  local addr log
  addr="$(effective_address)"
  echo "--- effective configuration ---"
  echo "  config file      : $CONFIG_FILE"
  echo "  listen address   : $addr"
  echo "  database         : data/pools.db (driver from store selection)"
  echo "  store backend    : $(current_store)"
  echo "  log directory    : $LOG_DIR"
  log="$(latest_service_log)"
  echo "  service log      : $(basename "${log:-none}")"
  echo "  launcher log     : $LAUNCHER_LOG"
  echo "  launcher env file: $LAUNCHER_ENV"
  echo "  binary           : $BINARY"
  echo "  mode             : $(current_mode | tr '[:lower:]' '[:upper:]')"
  echo "  launcher env (secrets redacted):"
  if [[ -f "$LAUNCHER_ENV" ]]; then
    awk '
      BEGIN { FS="\""; OFS="\"" }
      /"POOLS_ADDR"|"POOLS_DEBUG"|"POOLS_LOG_LEVEL"|"POOLS_STORE"|"updatedAt"|"_meta"/ { print "    " $0; next }
      /^[[:space:]]*"([A-Za-z0-9_]+)"[[:space:]]*:[[:space:]]*"([^"]*)"(.*)/ {
        sub(/:[[:space:]]*"[^"]*"/, ": \"<redacted>\"")
        print "    " $0
        next
      }
      /^[[:space:]]*[{}]/ { next }
      { print "    " $0 }
    ' "$LAUNCHER_ENV"
  else
    echo "    (empty)"
  fi
  echo "  store catalog:"
  for s in sqlite postgres json csv redis; do
    printf '    %-9s %-12s %s\n' "$s" "$(store_status "$s")" "$(store_note "$s")"
  done
  return 0
}

do_store() {
  local preset="${1:-}" choice status
  echo "--- store backend selection ---"
  for s in sqlite postgres json csv redis; do
    printf '  %-9s %-12s %s\n' "$s" "$(store_status "$s")" "$(store_note "$s")"
  done
  echo "  current selection: $(current_store) (written as POOLS_STORE into data/launcher.env.json)"
  choice="$preset"
  if [[ -z "$choice" ]]; then
    read -r -p "Select store backend (name, blank to keep current): " choice || choice=""
  fi
  if [[ -z "$choice" ]]; then echo "Selection unchanged."; return 0; fi
  choice="$(printf '%s' "$choice" | tr '[:upper:]' '[:lower:]')"
  status="$(store_status "$choice")"
  if [[ "$status" == "unknown" ]]; then
    echo "Unknown store backend '$choice'. Valid names: sqlite postgres json csv redis." >&2
    return 1
  fi
  env_set POOLS_STORE "$choice"
  echo "Store backend set to $choice (POOLS_STORE in data/launcher.env.json). The next start reads it."
  if [[ "$status" == "missing" ]]; then echo "Warning: this Go build has no $choice adapter; starting with it will be refused." >&2; fi
  if [[ "$status" == "stub" ]]; then echo "Warning: the $choice adapter is a skeleton." >&2; fi
  return 0
}

do_diagnostics() {
  local pid addr port size
  pid="$(core_pid)"
  addr="$(effective_address)"
  port="${addr##*:}"
  echo "--- diagnostics ---"
  echo "  host            : $(uname -srm)"
  echo "  project root    : $PROJECT_ROOT"
  if have go; then
    echo "  go              : $(go version)"
    echo "  go env          : GOMODCACHE=$(go env GOMODCACHE)"
    echo "  go env          : GOCACHE=$(go env GOCACHE)"
    echo "  go env          : GOTOOLCHAIN=$(go env GOTOOLCHAIN)"
  else
    echo "  go              : NOT FOUND on PATH (build/test will fail)"
  fi
  if [[ -x "$BINARY" ]]; then
    size="$(stat -c %s "$BINARY" 2>/dev/null)"
    echo "  binary          : $BINARY (${size:-0} bytes)"
  else
    echo "  binary          : not built"
  fi
  echo "  pid file        : $PID_FILE -> $(tr -d '\n' < "$PID_FILE" 2>/dev/null || echo absent)"
  echo "  process         : $([[ -n "$pid" ]] && echo "running (PID $pid)" || echo 'not running')"
  if [[ -n "$pid" ]]; then
    echo "  health          : $(health_json "$addr")"
  else
    echo "  health          : not reachable"
  fi
  if have ss; then
    if ss -ltn 2>/dev/null | grep -q ":$port "; then
      echo "  port $port        : listening"
    else
      echo "  port $port        : not listening"
    fi
  fi
  echo "  launcher log    : $LAUNCHER_LOG"
  echo "  launcher env    : $LAUNCHER_ENV"
  echo "  disk free       : $(df -h "$PROJECT_ROOT" 2>/dev/null | awk 'NR==2{print $4}')"
  return 0
}

do_test() {
  echo "--- build + test ---"
  go build ./... || return 1
  echo "go build ./...   : OK"
  go vet ./... || return 1
  echo "go vet ./...     : OK"
  go test ./... || return 1
  echo "go test ./...    : OK"
  return 0
}

# -------------------------------------------------------------------- main ----
START_MS="$(date +%s%3N)"

usage() {
  cat <<'USAGE'
pools-core launcher (scripts/pools-core.sh)

  start | start-debug    start the service in debug mode (POOLS_LOG_LEVEL=debug, POOLS_DEBUG=1)
  start-normal           start the service in normal mode (POOLS_LOG_LEVEL=info, POOLS_DEBUG=0)
  stop                   stop the verified local process
  restart                stop then start again in the current mode
  status                 PID, port, uptime and the /healthz JSON body
  logs [N]               last N lines of the newest service log (default 60)
  tail [level]           follow the newest service log; optional level filter
  config                 effective configuration and launcher env file
  store [name]           select the store backend (sqlite|postgres|json|csv|redis)
  diagnostics            toolchain, binary, pid, port, health and log locations
  test                   go build ./... + go vet ./... + go test ./...
USAGE
}

for a in "$@"; do
  if [[ "$a" == "--allow-remote" || "$a" == "-AllowRemote" ]]; then
    export ALLOW_REMOTE=1
  fi
done

action="${1:-help}"
arg1="${2:-}"
arg2="${3:-}"

case "$action" in
  start|start-debug)   do_start debug ;;
  start-normal)        do_start normal ;;
  stop)                do_stop ;;
  restart)             do_stop; do_start ;;
  status)              do_status ;;
  logs)                do_logs "${arg1:-60}" ;;
  tail)                do_tail "$arg1" ;;
  config)              do_config ;;
  store)               do_store "$arg1" ;;
  diagnostics)         do_diagnostics ;;
  test)                do_test ;;
  help|-h|--help)      usage; exit 0 ;;
  *) echo "Unknown action '$action'. Run '$0 help'." >&2; exit 2 ;;
esac
rc=$?
if [[ $rc -eq 0 ]]; then result=ok; else result=error; fi
launcher_log "$action" "$result" "$(( $(date +%s%3N) - START_MS ))" ""
exit $rc
