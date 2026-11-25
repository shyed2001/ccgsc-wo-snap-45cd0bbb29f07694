# F10b developer guide: verify CCGSC worker lease tokens

## 1. Overview and threat model

CCGSC Thread and Process pool web workers run in sandboxed iframes. Their origin is `null`; browsers do not expose cookies for such requests. Workers call the developer's own HTTPS or WSS endpoint directly.

Every request carries:

```http
Authorization: Bearer <token>
```

For WSS, the browser cannot set handshake headers. The worker sends this as its first application message:

```json
{"type":"auth","token":"<token>"}
```

The default token is a compact JWS signed with Ed25519:

- Header: `alg` is `EdDSA`; `kid` identifies the public key.
- Claims:
  - `iss`: exactly `ccgsc`
  - `aud`: the developer application ID
  - `owner`: the approved developer key owner (required to distinguish identical app IDs)
  - `purpose`: exactly `worker` for workload access
  - `jobId`, `taskId`, `policyVersion`: bound job, task and approved policy version
  - `sub`: the worker ID
  - `lease`: the lease ID
  - `pool`: `thread` or `process`
  - `iat`: issued-at time
  - `exp`: expiration time
  - `jti`: unique token ID

Public keys are published at:

```text
https://<ccgsc-host>/.well-known/ccgsc-jwks.json
```

Cache the JWKS for 10 minutes. Refetch it when a token contains an unknown `kid`.

### What the token proves

A valid token proves that CCGSC issued it against a then-live worker lease belonging to the configured developer application. Offline signature validation does not prove that the lease has not been cancelled since issuance. Enforce expiry and developer-side revocation or authorization state where required.

### What the token does not prove

It does not prove:

- that the worker's result is correct;
- that the worker behaves honestly;
- that a request is safe from application-level abuse;
- that the token is usable outside the lease and audience for which it was issued.

The contributor can read the bearer token. Therefore:

- scope it to one application;
- keep its lifetime short;
- renew it with the lease;
- bind authorization to the authenticated `sub`, `lease`, and, where applicable, `jti`;
- do not treat possession of the token as proof of a human identity.

---

## 2. CORS for `Origin: null`

For HTTPS endpoints, allow the sandboxed iframe's opaque origin explicitly:

```http
Access-Control-Allow-Origin: null
```

Do not combine `Access-Control-Allow-Origin: null` with credentialed CORS. These workers do not use cookies.

Allow the authorization header:

```http
Access-Control-Allow-Methods: GET, POST, OPTIONS
Access-Control-Allow-Headers: Authorization, Content-Type
Access-Control-Max-Age: 600
```

Example OPTIONS response:

```http
HTTP/1.1 204 No Content
Access-Control-Allow-Origin: null
Access-Control-Allow-Methods: GET, POST, OPTIONS
Access-Control-Allow-Headers: Authorization, Content-Type
Access-Control-Max-Age: 600
Content-Length: 0
```

The preflight request may contain:

```http
Origin: null
Access-Control-Request-Method: POST
Access-Control-Request-Headers: authorization,content-type
```

Return the same CORS headers on the actual response:

```http
Access-Control-Allow-Origin: null
```

For WebSocket connections, CORS does not apply. Authenticate the first application message as described below.

---

## 3. Verification checklist

Before accepting a token:

1. Parse exactly three JWS segments: `header.payload.signature`.
2. Require header `alg` to be exactly `EdDSA`; reject `none` and every other algorithm.
3. Require a non-empty header `kid`.
4. Resolve `kid` through the CCGSC JWKS.
5. Refetch the JWKS once if `kid` is unknown; reject the token if it remains unknown.
6. Verify the Ed25519 signature over:

   ```text
   ASCII(BASE64URL(header) || "." || BASE64URL(payload))
   ```

7. Require all claims:
   - `iss == "ccgsc"`
   - `aud == <developer app id>`
   - `owner == <approved developer key owner>`
   - `purpose == "worker"`
   - non-empty `sub`
   - non-empty `lease`
   - `pool` is `thread` or `process`
   - `jti` is non-empty
8. Validate times:
   - reject if `exp <= now`;
   - reject if `nbf` is present and `nbf > now + 30s`;
   - reject if `iat` is present and `iat > now + 30s`;
   - optionally reject implausibly old `iat` values according to lease policy.
9. Optionally cache accepted `jti` values until `exp` and reject reuse.
10. Rate-limit by authenticated `sub`, and separately by source address where appropriate.
11. Do not log or persist bearer tokens.

Allow a limited future-clock tolerance for iat/nbf if needed, but never accept an expired token. Require iat and exp, with exp > iat and exp - iat <= 600 seconds.

---

## 4. Complete verification examples

The examples below accept a token from an `Authorization: Bearer ...` header. Configure the application ID, CCGSC host, and accepted `pool` values.

### Go

Standard library only (`crypto/ed25519`); parses the JWK set directly. The original handoff example was head-verified on 2026-11-24; this revision adds the owner/purpose and strict expiry checks.

```go
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const appID = "app-example"
const ownerID = "replace-with-approved-owner"

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	X   string `json:"x"`
}

type jwks struct{ Keys []jwk `json:"keys"` }

type claims struct {
	Owner string `json:"owner"`
	Purpose string `json:"purpose"`
	Iss  string `json:"iss"`
	Aud  string `json:"aud"`
	Sub  string `json:"sub"`
	Lease string `json:"lease"`
	Pool string `json:"pool"`
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
	Nbf  int64  `json:"nbf"`
	Jti  string `json:"jti"`
}

type keyCache struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey
	until time.Time
}

func (c *keyCache) get(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	c.mu.RLock()
	key, ok := c.keys[kid]
	fresh := time.Now().Before(c.until)
	c.mu.RUnlock()
	if ok && fresh {
		return key, nil
	}
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok = c.keys[kid]
	if !ok {
		return nil, errors.New("unknown kid")
	}
	return key, nil
}

func (c *keyCache) refresh(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		"https://ccgsc.example/.well-known/ccgsc-jwks.json", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS status %d", resp.StatusCode)
	}
	var set jwks
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return err
	}
	keys := make(map[string]ed25519.PublicKey)
	for _, k := range set.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Kid == "" {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			continue
		}
		keys[k.Kid] = ed25519.PublicKey(x)
	}
	c.mu.Lock()
	c.keys, c.until = keys, time.Now().Add(10*time.Minute)
	c.mu.Unlock()
	return nil
}

func verify(raw string, now time.Time) (*claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid JWS")
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("invalid header encoding")
	}
	var head struct { Alg, Kid string }
	if json.Unmarshal(h, &head) != nil || head.Alg != "EdDSA" || head.Kid == "" {
		return nil, errors.New("invalid header")
	}
	p, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid payload encoding")
	}
	var cl claims
	if json.Unmarshal(p, &cl) != nil {
		return nil, errors.New("invalid claims")
	}
	if cl.Iss != "ccgsc" || cl.Aud != appID || cl.Owner != ownerID || cl.Purpose != "worker" || cl.Sub == "" ||
		cl.Lease == "" || cl.Jti == "" ||
		(cl.Pool != "thread" && cl.Pool != "process") {
		return nil, errors.New("invalid claims")
	}
	if cl.Exp == 0 || cl.Exp <= now.Unix() || cl.Iat <= 0 || cl.Exp <= cl.Iat || cl.Exp-cl.Iat > 600 {
		return nil, errors.New("expired")
	}
	if cl.Nbf != 0 && cl.Nbf > now.Add(30*time.Second).Unix() {
		return nil, errors.New("not yet valid")
	}
	if cl.Iat != 0 && cl.Iat > now.Add(30*time.Second).Unix() {
		return nil, errors.New("issued in the future")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("invalid signature encoding")
	}
	key, err := cache.get(context.Background(), head.Kid)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, errors.New("invalid signature")
	}
	return &cl, nil
}

var cache = &keyCache{keys: map[string]ed25519.PublicKey{}}

func main() {
	http.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		cl, err := verify(strings.TrimPrefix(h, "Bearer "), time.Now())
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, "worker=%s lease=%s pool=%s", cl.Sub, cl.Lease, cl.Pool)
	})
	_ = http.ListenAndServe(":8080", nil)
}
```

For production, replace the example host and app ID, use a real context with a timeout for JWKS retrieval.

### Node.js 22

Uses Node's built-in `crypto` module.

```js
import { createPublicKey, verify } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';

const APP_ID = 'app-example';
const OWNER_ID = 'replace-with-approved-owner';
const JWKS_URL = 'https://ccgsc.example/.well-known/ccgsc-jwks.json';
const SKEW_MS = 30_000;

let cached = { keys: new Map(), expires: 0 };

async function getKey(kid) {
  if (Date.now() < cached.expires && cached.keys.has(kid)) {
    return cached.keys.get(kid);
  }

  const response = await fetch(JWKS_URL, { signal: AbortSignal.timeout(5_000) });
  if (!response.ok) throw new Error(`JWKS status ${response.status}`);
  const jwks = await response.json();
  const keys = new Map();
  for (const jwk of jwks.keys ?? []) {
    if (jwk.kty !== 'OKP' || jwk.crv !== 'Ed25519' || !jwk.kid) continue;
    keys.set(jwk.kid, createPublicKey({ key: jwk, format: 'jwk' }));
  }
  cached = { keys, expires: Date.now() + 10 * 60_000 };
  if (!keys.has(kid)) throw new Error('unknown kid');
  return keys.get(kid);
}

function decode(value) {
  return Buffer.from(value, 'base64url');
}

export async function verifyToken(raw) {
  const parts = raw.split('.');
  if (parts.length !== 3) throw new Error('invalid JWS');

  const header = JSON.parse(decode(parts[0]).toString('utf8'));
  if (header.alg !== 'EdDSA' || typeof header.kid !== 'string' || !header.kid) {
    throw new Error('invalid header');
  }

  const claims = JSON.parse(decode(parts[1]).toString('utf8'));
  if (claims.iss !== 'ccgsc' || claims.aud !== APP_ID || claims.owner !== OWNER_ID || claims.purpose !== 'worker' ||
      typeof claims.sub !== 'string' || !claims.sub ||
      typeof claims.lease !== 'string' || !claims.lease ||
      typeof claims.jti !== 'string' || !claims.jti ||
      !['thread', 'process'].includes(claims.pool)) {
    throw new Error('invalid claims');
  }

  const now = Date.now();
  if (!Number.isSafeInteger(claims.exp) || !Number.isSafeInteger(claims.iat) || claims.iat <= 0 || claims.exp <= claims.iat || claims.exp - claims.iat > 600 || claims.exp * 1000 <= now) {
    throw new Error('expired');
  }
  if (claims.nbf !== undefined &&
      (!Number.isFinite(claims.nbf) || claims.nbf * 1000 > now + SKEW_MS)) {
    throw new Error('not yet valid');
  }
  if (claims.iat !== undefined &&
      (!Number.isFinite(claims.iat) || claims.iat * 1000 > now + SKEW_MS)) {
    throw new Error('issued in the future');
  }

  let key;
  try {
    key = await getKey(header.kid);
  } catch (error) {
    if (cached.keys.has(header.kid)) throw error;
    cached = { keys: new Map(), expires: 0 };
    key = await getKey(header.kid);
  }

  // crypto.verify(algorithm, data, key, signature)
  const valid = verify(null, Buffer.from(`${parts[0]}.${parts[1]}`), key,
    decode(parts[2]));
  if (!valid) throw new Error('invalid signature');
  return claims;
}

export async function handler(request) {
  const match = /^Bearer (.+)$/.exec(request.headers.get('authorization') ?? '');
  if (!match) return new Response('unauthorized', { status: 401 });
  try {
    const claims = await verifyToken(match[1]);
    return new Response(JSON.stringify({
      worker: claims.sub, lease: claims.lease, pool: claims.pool
    }), {
      headers: {
        'content-type': 'application/json',
        'access-control-allow-origin': 'null',
        'access-control-allow-methods': 'GET, POST, OPTIONS',
        'access-control-allow-headers': 'Authorization, Content-Type'
      }
    });
  } catch {
    return new Response('unauthorized', { status: 401 });
  }
}
```

The unknown-`kid` path above refetches only when the cached key set did not contain the requested `kid`; a production implementation should also avoid replacing a healthy cache with an empty set after a failed refetch.

### Python 3.12

Requires:

```text
cryptography>=42
```

```python
import base64
import json
import time
import urllib.request
from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

APP_ID = "app-example"
OWNER_ID = "replace-with-approved-owner"
JWKS_URL = "https://ccgsc.example/.well-known/ccgsc-jwks.json"
SKEW = 30


def b64url(value: str) -> bytes:
    return base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))


_cache = {"keys": {}, "expires": 0.0}


def get_key(kid: str) -> Ed25519PublicKey:
    now = time.time()
    if now < _cache["expires"] and kid in _cache["keys"]:
        return _cache["keys"][kid]

    with urllib.request.urlopen(JWKS_URL, timeout=5) as response:
        document = json.load(response)

    keys = {}
    for jwk in document.get("keys", []):
        if jwk.get("kty") != "OKP" or jwk.get("crv") != "Ed25519":
            continue
        if not jwk.get("kid"):
            continue
        try:
            public_bytes = b64url(jwk["x"])
            if len(public_bytes) != 32:
                continue
            keys[jwk["kid"]] = Ed25519PublicKey.from_public_bytes(public_bytes)
        except (KeyError, ValueError):
            continue

    _cache["keys"] = keys
    _cache["expires"] = now + 600
    if kid not in keys:
        raise ValueError("unknown kid")
    return keys[kid]


def verify_token(raw: str) -> dict:
    parts = raw.split(".")
    if len(parts) != 3:
        raise ValueError("invalid JWS")

    header = json.loads(b64url(parts[0]))
    if header.get("alg") != "EdDSA" or not header.get("kid"):
        raise ValueError("invalid header")

    claims = json.loads(b64url(parts[1]))
    if (claims.get("iss") != "ccgsc" or claims.get("aud") != APP_ID
            or claims.get("owner") != OWNER_ID or claims.get("purpose") != "worker"):
        raise ValueError("invalid issuer or audience")
    if not all(isinstance(claims.get(k), str) and claims[k]
               for k in ("sub", "lease", "jti")):
        raise ValueError("invalid worker claims")
    if claims.get("pool") not in ("thread", "process"):
        raise ValueError("invalid pool")

    now = time.time()
    if (type(claims.get("exp")) is not int or type(claims.get("iat")) is not int
            or claims["iat"] <= 0 or not 0 < claims["exp"] - claims["iat"] <= 600):
        raise ValueError("invalid expiration")
    if claims["exp"] <= now:
        raise ValueError("expired")
    if claims.get("nbf") is not None and claims["nbf"] > now + SKEW:
        raise ValueError("not yet valid")
    if claims.get("iat") is not None and claims["iat"] > now + SKEW:
        raise ValueError("issued in the future")

    try:
        key = get_key(header["kid"])
    except ValueError:
        _cache["expires"] = 0
        key = get_key(header["kid"])

    try:
        key.verify(b64url(parts[2]), f"{parts[0]}.{parts[1]}".encode("ascii"))
    except InvalidSignature as exc:
        raise ValueError("invalid signature") from exc
    return claims
```

---

## 5. WSS authentication: first-message pattern

The worker must send the authentication message as the first WebSocket application message:

```json
{"type":"auth","token":"<JWS-or-opaque-token>"}
```

Server procedure:

1. Accept the WebSocket connection.
2. Start a 5-second authentication deadline.
3. Require the first application message to be the `auth` message.
4. Reject malformed JSON, missing `type`, or an unexpected first message.
5. Verify the token according to the selected token mode.
6. On success, mark the connection authenticated and process subsequent messages.
7. On failure or timeout, close the connection with an appropriate WebSocket close code.
8. Do not process work messages before authentication succeeds.

Example server-side logic:

```text
on connection:
    timer = start_timer(5 seconds)

    on first message:
        stop(timer)
        if message.type != "auth":
            close(1008, "authentication required")
            return
        verify(message.token)
        if verification failed:
            close(1008, "invalid token")
            return
        mark_authenticated()

    on timer:
        close(1008, "authentication timeout")
```

The worker must not send work payloads before receiving an application-level authentication acknowledgment until receiving the required acknowledgment:

```json
{"type":"auth-ok"}
```

The WSS endpoint must use TLS. The bearer token must not be placed in the URL, query string, or WebSocket subprotocol name.

---

## 6. Developer token service mode

Instead of CCGSC issuing a JWS directly to the worker, the developer can provide a token service. CCGSC calls the developer's service server-side for each lease. CCGSC passes the returned opaque token to the worker. The worker then sends that token using the same HTTPS or WSS authentication mechanism, but the developer validates it according to its own token format.

### Request from CCGSC to the developer token service

```http
POST /ccgsc/token HTTP/1.1
Content-Type: application/json
Authorization: Bearer <ccgsc-service-token>
```

The service token is a separate EdDSA JWS using the same JWKS, with purpose `service`, audience `ccgsc:service:<appId>`, exact configured owner, and expiry at most 60 seconds (also bounded by the lease). Verify signature, purpose, audience, owner, expiration and match sub/lease/pool against worker_id/lease_id/pool in the body. Check app_id against the approved application and bound request validity to the live lease lifetime. Never accept a worker or runner token at this endpoint.

Request JSON:

```json
{
  "app_id": "app-example",
  "worker_id": "worker-123",
  "lease_id": "lease-456",
  "pool": "thread",
  "issued_at": 1710000000,
  "expires_at": 1710000600
}
```

All fields are required except where explicitly documented otherwise:

- `app_id`: developer application ID;
- `worker_id`: worker identifier;
- `lease_id`: lease identifier;
- `pool`: `thread` or `process`;
- `issued_at`: Unix timestamp in seconds;
- `expires_at`: Unix timestamp in seconds.

### Response from the developer token service

```http
HTTP/1.1 200 OK
Content-Type: application/json
Cache-Control: no-store
```

```json
{
  "token": "<opaque-token>",
  "expires_at": 1710000600
}
```

Contract requirements:

- The response token must be non-empty and unique enough to identify the lease or authorization context.
- `expires_at` must not exceed the lease expiration.
- The token must be scoped to `app_id`, `worker_id`, and `lease_id`.
- The developer service should return a short expiration, normally no longer than the lease.
- The token must be usable only by the intended worker lease.
- CCGSC must reject an invalid or unsuccessful token-service response.
- The worker must never receive the CCGSC service credential.
- The developer's API must verify the opaque token by looking up its server-side state or verifying its signature and claims.
- A failed token-service call must fail closed; it must not cause the worker to receive an unsigned or unverified credential.

Example opaque token payload before signing or server-side storage:

```json
{
  "v": 1,
  "app_id": "app-example",
  "sub": "worker-123",
  "lease": "lease-456",
  "iat": 1710000000,
  "exp": 1710000600,
  "jti": "opaque-1710000000-lease-456"
}
```

The exact opaque-token format is developer-specific.

---

## 7. Test vectors

Test vectors are generated by the CCGSC repository test. Do not invent values here.

Use the repository-provided vectors to test:

- valid `thread` and `process` tokens;
- invalid signature;
- `alg` other than `EdDSA`;
- missing or unknown `kid`;
- wrong issuer;
- wrong audience;
- expired token;
- `nbf` in the future;
- `iat` in the future;
- missing required claims;
- invalid `pool`;
- malformed compact JWS;
- base64url decoding failures;
- WSS authentication timeout;
- WSS authentication as a non-first message;
- developer token-service request and response contract;
- unknown-`kid` JWKS refetch behavior.



## 8. F10b implementation contract

- Egress is **default deny**. An administrator approves a named app per pool and developer key owner; use HTTPS/WSS origins (standard port 443, no path, wildcard, userinfo or IP literal). Origins grant access to paths on that origin; no claim of path-level CSP isolation. Optional token-service URL is an exact HTTPS path.
- Admin APIs: PUT /admin/v1/thread-apps/{appId} with owner, maxWorkers, and egress; PUT /admin/v1/process-apps/{appId} with owner, maxTabs, and egress. The egress object is { "endpoints": ["https://api.example.com","wss://socket.example.com"], "tokenServiceUrl": "https://api.example.com/ccgsc/token" }; omit tokenServiceUrl for CCGSC-signed worker tokens. An empty endpoints list disables egress. Egress changes are refused while active quota bindings remain.
- One named app per physical tab across both pools, with a durable shared fence; Thread W is worker slots and Process K is whole tabs. Admin tab release: GET/PUT /admin/v1/pools/{tp|pp}/tab-release, mode idle-timeout (default 30 s) or immediate-when-idle. The idle interval starts when idle is observed during claim or heartbeat, not an assumed wall-clock completion time.
- Contributor must opt in to the displayed exact policy versions. Egress requires a distinct configured run origin and a live, signed runner grant. The runner grant is separate from worker and service credentials. Exclude runner grant query strings from production access logs; no bearer tokens in logs. A newly loaded frame receives only that app's CSP.
- JS module ABI: call `await ctx.egress.fetch("https://api.example.com/work", {method:"POST",body:"...",headers:{"Content-Type":"text/plain"}})`; response has ok, status, bytes, text(), json(). For WSS call `ctx.egress.connect("wss://socket.example.com/work",{onReady,onMessage,onError,onClose})` and use returned send()/close(); auth is automatic. The raw WASM byte ABI has no network imports; use a JS module adapter. No global fetch/WebSocket is restored.
- Browser wrappers limit 60 operations/minute per lease, request/message bodies to 256 KiB and responses to 1 MiB, one WSS connection, HTTPS timeout <=15 s, no redirects or cookies. These are client controls, not a tamper-proof quota: developer services must enforce their own authenticated server-side limits. Renewed tokens replace the old credential and close prior WSS connections. Short token lifetime is bounded by lease expiration, at most ten minutes.
- Server-side developer token service: no proxy or redirects; resolve and pin public destination IP, reject private/link-local/reserved addresses, verify TLS, timeout 5 s, limit response to 8 KiB and returned token to 4 KiB. Browser origin approvals are not a DNS-rebinding guarantee; approve only controlled trusted DNS origins.
- Existing no-app legacy workloads remain network-disabled. No open/unrestricted mode, developer master secret, or platform-owned app database is introduced.
- Acceptance evidence: targeted Go tests exercise real PostgreSQL cross-engine fencing, consent, lease grants, token service responses and CSP headers; JS tests exercise HTTPS/WSS wrappers. An actual deployed developer endpoint/browser run remains separate from these unit/integration tests. No deployment authorized.
