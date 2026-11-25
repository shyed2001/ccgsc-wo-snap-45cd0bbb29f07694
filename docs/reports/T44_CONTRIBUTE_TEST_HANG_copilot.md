# T44 contribute.test.mjs hang

Cause: `ContributeApp.setupRunnerFrame()` created a new `MessageChannel` for
each handshake attempt without closing the previous ports. It also left the
retry `setTimeout` untracked. The test suite completed its 66 assertions, but
the open `MessagePort`s kept Node's event loop alive until the test timeout.

Fix: `contribute.js` now closes superseded channel ports, tracks and cancels the
handshake retry timer, and exposes `disposeRunnerFrame()` for deterministic
cleanup. The two tests that exercise iframe setup register their apps in a
suite-level `after()` cleanup.

Validation:

```text
RUN 1 exit=0
# tests 66
# pass 66
# fail 0
# cancelled 0
# duration_ms 768.346

RUN 2 exit=0
# tests 66
# pass 66
# fail 0
# cancelled 0
# duration_ms 792.3741

RUN 3 exit=0
# tests 66
# pass 66
# fail 0
# cancelled 0
# duration_ms 737.3464
```

Additional checks: `web.test.mjs` 17/17 passed; `pages.test.mjs` 17/17
passed; JavaScript syntax checks passed.

DONE T44
