# Adapter Development Guide

An **adapter** is the thin layer that exposes your agent's workflow lifecycle over the Crash-Test Guard protocol. Crash-Test Guard never imports your agent code and never interprets your checkpoint format — it only speaks HTTP/JSON to the six endpoints defined in [protocol.md](protocol.md).

This guide covers what an adapter must do, the three failure modes that break crash tests, and a complete but minimal Python skeleton.

---

## Architecture

```
Crash-Test Guard ──HTTP/JSON──▶ Adapter ──▶ Your agent / graph / workflow
                                    │
                                    ├── checkpoint store (SQLite, Postgres, files)
                                    ├── event buffer (in-memory + persisted sequence)
                                    └── external services (with idempotency keys)
```

Crash-Test Guard is responsible for:

- Starting the agent process or container (`agent.command`)
- Sending `SIGKILL` / terminating the container at the trigger point
- Restarting and calling `/resume`
- Comparing the baseline run against the crash run

The adapter is responsible for everything stateful.

---

## Protocol requirements (6 endpoints)

All responses must include `"protocol_version": "1.0"`. Errors use [RFC 7807](https://datatracker.ietf.org/doc/html/rfc7807) ProblemDetails.

### 1. `GET /health`

Return readiness plus version bounds. Crash-Test Guard negotiates here before doing anything else.

```json
{"protocol_version": "1.0", "agent_version": "0.1.0", "status": "healthy"}
```

Rules:
- Answer only after the checkpoint store is reachable.
- Return `503` with a ProblemDetails body if the agent is not ready; Crash-Test Guard retries until `startup_timeout`.
- Declare `MIN_SUPPORTED_VERSION` / `MAX_SUPPORTED_VERSION`; reject out-of-range versions with `426`.

### 2. `POST /workflow/start`

Create a workflow, persist its initial state, and return the id.

```json
{"input": {"email_id": "msg_123", "customer_id": "cust_456"}}
```

```json
{"protocol_version": "1.0", "workflow_id": "wf_abc123", "status": "running"}
```

Rules:
- The `workflow_id` must be **unique per test run** — Crash-Test Guard runs a baseline and a crash execution, and their state must not be shared.
- Persist the initial state before returning, so a crash immediately after `start` is recoverable.
- Execution may run asynchronously; return promptly.

### 3. `GET /workflow/{id}/state`

Report progress from durable state, not from in-memory variables.

```json
{
  "protocol_version": "1.0",
  "workflow_id": "wf_abc123",
  "status": "running",
  "current_step": "process_payment",
  "completed_steps": ["read_email", "generate_invoice"],
  "step_outputs": {"generate_invoice": {"invoice_id": "inv_789", "amount": 99.00}}
}
```

Rules:
- `completed_steps` must be ordered and append-only. The verifier checks `post ⊇ pre`.
- `status` is one of `running`, `completed`, `failed`, `paused`.
- Return values reconstructed from the checkpoint store so that `state` is correct after restart.

### 4. `GET /workflow/{id}/checkpoint`

Return the latest checkpoint **envelope**.

```json
{
  "protocol_version": "1.0",
  "workflow_id": "wf_abc123",
  "checkpoint_id": "cp_456",
  "sequence": 12,
  "created_at": "2026-01-15T10:30:00Z",
  "payload": {"format": "application/octet-stream", "data": "YmFzZTY0..."}
}
```

Rules:
- Save a checkpoint **after every durable state transition**, and always before an external side effect.
- `sequence` must increase monotonically across the whole workflow lifetime, including across restarts.
- Never mutate an existing `checkpoint_id`; save a new one.

### 5. `GET /workflow/{id}/events`

Return the ordered event history.

```json
{
  "protocol_version": "1.0",
  "workflow_id": "wf_abc123",
  "events": [
    {"sequence": 1, "type": "workflow_started", "step_id": null, "timestamp": "2026-01-15T10:29:45Z", "data": {}},
    {"sequence": 2, "type": "step_started", "step_id": "read_email", "timestamp": "2026-01-15T10:29:45Z", "data": {}},
    {"sequence": 3, "type": "step_completed", "step_id": "read_email", "timestamp": "2026-01-15T10:29:46Z", "data": {"subject": "Invoice Request"}}
  ]
}
```

Standard event types: `workflow_started`, `step_started`, `step_completed`, `checkpoint_saving`, `checkpoint_saved`, `external_call_start`, `external_call_completed`, `result_saving`, `workflow_completing`, `workflow_completed`.

Rules:
- Emit the event **before** the corresponding work begins for `*_start` / `*_saving` events, and immediately after for `*_completed` / `*_saved`.
- Append-only, strictly increasing, no duplicates. Gaps are allowed.
- Persist events (or the latest sequence plus the tail) so triggers still work after restart.

### 6. `POST /workflow/{id}/resume`

Resume from a checkpoint payload supplied by Crash-Test Guard.

```json
{"checkpoint": "YmFzZTY0X2VuY29kZWRfb3BhcXVlX2NoZWNrcG9pbnQ="}
```

```json
{"protocol_version": "1.0", "status": "resumed", "workflow_id": "wf_abc123"}
```

Rules:
- Either accept the inline payload **or** ignore it and reload the latest checkpoint from your own store — both are valid, but be consistent.
- Resume must be **idempotent**: calling `/resume` twice must not repeat side effects.
- Resume from the last durable step, not from the beginning. Restarting from step 1 is the most common adapter bug.
- Return `400` for undecodable payloads, `409` if the workflow is already completed, `422` if the checkpoint is incompatible with the current agent version.

---

## Checkpointing: payload is opaque

Crash-Test Guard treats `payload.data` as an opaque base64 blob. This means:

- Use **any** serialization: pickle, JSON, msgpack, protobuf, custom binary.
- The `format` field is informational metadata for your own tooling. Set it honestly (`application/json`, `application/octet-stream`, `application/x-msgpack`).
- Never rely on Crash-Test Guard to migrate, validate, or repair payloads.

What Crash-Test Guard *does* compare is envelope metadata (`workflow_id`, `sequence`, `checkpoint_id` presence), so keep metadata semantically stable. It ignores `created_at` and `checkpoint_id` in semantic diffs.

### Checkpointing strategy

Checkpoint at the boundaries that matter:

| Point | Why |
|-------|-----|
| After workflow start | Enables recovery from an immediate crash |
| After each completed step | Bounds replay to a single step |
| Before each external side effect | Provides a resume point that does not repeat the call |
| After recording a side-effect result | Prevents re-issuing the call on resume |
| Before final result persistence | Makes finalization crash-safe |

The classic bug: checkpointing *before* the external call and then resuming from that checkpoint without any record of the call — the agent re-issues the payment. Solve it with an idempotency key and an operation record written in the same durable transaction as the checkpoint.

### Example: snapshot and restore

```python
import base64, json, sqlite3

def save_checkpoint(workflow_id: str, state: dict, sequence: int) -> str:
    payload = base64.b64encode(json.dumps(state).encode()).decode()
    checkpoint_id = f"cp_{sequence}"
    with sqlite3.connect("checkpoints.db") as db:
        db.execute(
            "INSERT OR REPLACE INTO checkpoint (workflow_id, checkpoint_id, sequence, created_at, format, data) "
            "VALUES (?, ?, ?, datetime('now'), ?, ?)",
            (workflow_id, checkpoint_id, sequence, "application/json", payload),
        )
    return checkpoint_id

def load_checkpoint(workflow_id: str) -> dict:
    with sqlite3.connect("checkpoints.db") as db:
        row = db.execute(
            "SELECT data FROM checkpoint WHERE workflow_id = ? ORDER BY sequence DESC LIMIT 1",
            (workflow_id,),
        ).fetchone()
    return json.loads(base64.b64decode(row[0])) if row else {}
```

---

## Event stream: sequence numbers are the contract

Sequence numbers replace sleep-and-hope timing. Crash-Test Guard waits for the exact `sequence` of the trigger event, then kills the process.

Requirements:

1. **Strictly increasing** across the workflow's entire lifetime — never reset on restart. Persist the counter alongside workflow state.
2. **Emitted at the right moment.** `step_started` is useless as a "before step" trigger if it is emitted after the step's work is already done.
3. **Durable.** After a `SIGKILL`, `/events` must still return the pre-crash history, otherwise the post-crash comparison and trigger bookkeeping break.

```python
class EventLog:
    def __init__(self, workflow_id: str):
        self.workflow_id = workflow_id
        self._events: list[dict] = []

    def emit(self, event_type: str, step_id: str | None = None, data: dict | None = None) -> int:
        sequence = (self._events[-1]["sequence"] + 1) if self._events else 1
        self._events.append({
            "sequence": sequence,
            "type": event_type,
            "step_id": step_id,
            "timestamp": now_iso8601(),
            "data": data or {},
        })
        persist_event(self.workflow_id, self._events[-1])  # survives SIGKILL
        return sequence
```

Call order for a step that touches an external service:

```python
log.emit("step_started", step_id="process_payment")
log.emit("external_call_start", step_id="process_payment")
result = payments.charge(idempotency_key=key, amount=99.00)
log.emit("external_call_completed", step_id="process_payment", data={"txn": result["transaction_id"]})
save_checkpoint(wf_id, state_after_payment, next_sequence)
log.emit("checkpoint_saved", step_id="process_payment")
log.emit("step_completed", step_id="process_payment")
```

### Pre-event barrier (optional, v1.1)

For zero-race triggering, an adapter may block after emitting a `*_start` event until Crash-Test Guard signals `CRASH_NOW` on a dedicated endpoint. This is an optional extension, not one of the six core endpoints. If you skip it, ensure events are emitted *before* the work so the kill lands in the intended window.

---

## Idempotency keys for side effects

Crash-Test Guard checks an **operation ledger**, not just whether the agent *intended* to be idempotent. The ledger is supplied by the test environment — mock services, test doubles, or an audit log.

Ledger entry:

```json
{
  "workflow_id": "wf_123",
  "operation_id": "payment_step_3",
  "operation_type": "payment",
  "idempotency_key": "payment_abc",
  "status": "committed",
  "result": {"transaction_id": "txn_789", "amount": 99.00},
  "timestamp": "2026-01-15T10:30:05Z",
  "sequence": 7
}
```

Adapter rules:

- Use a **stable** `operation_id`, typically `f"{workflow_id}:{step_id}"`. Derive it from durable state, not from a random value generated at call time — a random id on retry looks like a new operation.
- Use one `idempotency_key` per logical operation and reuse it verbatim on retry.
- Same `idempotency_key` must return the same `result`.
- Track `attempt_count` separately from `status: committed`. Retries are expected and are not duplicates; two *committed* entries with the same `operation_id` are.

Correct pattern — or nothing at all:

```python
key = f"{workflow_id}:process_payment"

existing = ledger.find(idempotency_key=key)
if existing and existing["status"] == "committed":
    payment = existing["result"]          # already done, do not call again
else:
    payment = payments.charge(idempotency_key=key, amount=99.00)

save_checkpoint(workflow_id, {**state, "payment": payment}, next_sequence)
```

Anti-patterns that fail the duplicate check:

| Anti-pattern | Result |
|--------------|--------|
| Key includes a timestamp or UUID generated per attempt | Duplicate committed operations |
| Checkpoint updated before the ledger commit | Payment re-issued on resume |
| In-memory dedup set only | Lost at `SIGKILL`, duplicate on restart |
| Key derived from the request payload including volatile fields | Same operation looks new after retry |

---

## Testing with `crash-test-guard`

Iterate in this order — each step isolates one class of bug.

1. **Protocol smoke test.** Start the agent and curl every endpoint.

   ```bash
   curl -s localhost:8080/health | jq
   curl -s -X POST localhost:8080/workflow/start -H 'content-type: application/json' \
     -d '{"input": {"email_id": "msg_1"}}' | jq
   curl -s localhost:8080/workflow/wf_abc123/events | jq
   ```

2. **Baseline run.** A scenario with no crash window or `crash_scenarios: []` must reach `completed`. If the baseline does not complete, crash results are meaningless.

3. **One crash window at a time.** Start with `after_external_op` — it exposes the duplicate-payment bug most directly.

   ```bash
   ./crash-test-guard test --config configs/crash-after-external-op.yaml --verbose
   ```

4. **All six windows.** Use the provided configs as a matrix:

   | Config | Window |
   |--------|--------|
   | `crash-before-checkpoint.yaml` | `checkpoint_saving` / `before_event` |
   | `crash-after-checkpoint.yaml` | `checkpoint_saved` / `after_event` |
   | `crash-before-external-op.yaml` | `external_call_start` / `before_event` |
   | `crash-after-external-op.yaml` | `external_call_completed` / `after_event` |
   | `crash-before-result-persistence.yaml` | `result_saving` / `before_event` |
   | `crash-during-finalization.yaml` | `workflow_completing` / `before_event` |

5. **Repeat for flakiness.** Run the suite several times; a trigger that depends on timing rather than `sequence` will fail intermittently.

6. **Wire into CI.** Gate the deploy on exit code `0`; treat `2` (ERROR) as an infrastructure problem, not a pass.

### Debugging failed checks

| Failed check | Likely adapter cause |
|--------------|----------------------|
| `fallback.on_missed` fired | `/events` missing the trigger type, or event emitted after the work |
| Checkpoint consistency | Non-durable state, or state written outside the checkpoint transaction |
| Duplicate side effects | Key regenerated per attempt, or ledger commit ordered after the checkpoint |
| Workflow completion | `/resume` restarts from step 1, or `status` never reaches `completed` |
| Monotonic progress | `completed_steps` rebuilt from partial in-memory state |

---

## Minimal Python adapter skeleton

A complete, runnable adapter. It uses SQLite for durable state and a fake payment ledger; replace `run_step` with your framework's executor.

```python
"""Minimal Crash-Test Guard adapter (FastAPI + SQLite)."""

import base64
import json
import sqlite3
from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException

PROTOCOL_VERSION = "1.0"
AGENT_VERSION = "0.1.0"

app = FastAPI()
conn = sqlite3.connect("adapter.db", check_same_thread=False)
conn.executescript(
    """
    CREATE TABLE IF NOT EXISTS workflow (
        workflow_id TEXT PRIMARY KEY,
        state TEXT NOT NULL,
        sequence INTEGER NOT NULL DEFAULT 0
    );
    CREATE TABLE IF NOT EXISTS event (
        workflow_id TEXT NOT NULL,
        sequence INTEGER NOT NULL,
        body TEXT NOT NULL,
        PRIMARY KEY (workflow_id, sequence)
    );
    CREATE TABLE IF NOT EXISTS operation (
        workflow_id TEXT NOT NULL,
        operation_id TEXT NOT NULL,
        idempotency_key TEXT NOT NULL,
        status TEXT NOT NULL,
        result TEXT,
        PRIMARY KEY (workflow_id, operation_id)
    );
    """
)


def now_iso8601() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def emit(workflow_id: str, event_type: str, step_id: str | None = None, data: dict | None = None) -> int:
    row = conn.execute(
        "SELECT sequence FROM workflow WHERE workflow_id = ?", (workflow_id,)
    ).fetchone()
    sequence = (row[0] if row else 0) + 1
    conn.execute(
        "INSERT INTO event (workflow_id, sequence, body) VALUES (?, ?, ?)",
        (
            workflow_id,
            sequence,
            json.dumps(
                {
                    "sequence": sequence,
                    "type": event_type,
                    "step_id": step_id,
                    "timestamp": now_iso8601(),
                    "data": data or {},
                }
            ),
        ),
    )
    conn.execute(
        "UPDATE workflow SET sequence = ? WHERE workflow_id = ?", (sequence, workflow_id)
    )
    conn.commit()
    return sequence


def save_checkpoint(workflow_id: str, state: dict) -> None:
    conn.execute(
        "UPDATE workflow SET state = ? WHERE workflow_id = ?",
        (base64.b64encode(json.dumps(state).encode()).decode(), workflow_id),
    )
    conn.commit()


def load_state(workflow_id: str) -> dict:
    row = conn.execute(
        "SELECT state FROM workflow WHERE workflow_id = ?", (workflow_id,)
    ).fetchone()
    if row is None:
        raise HTTPException(404, detail="workflow not found")
    return json.loads(base64.b64decode(row[0]))


def charge(workflow_id: str, amount: float) -> dict:
    operation_id = f"{workflow_id}:process_payment"
    key = f"payment:{workflow_id}"
    row = conn.execute(
        "SELECT status, result FROM operation WHERE workflow_id = ? AND operation_id = ?",
        (workflow_id, operation_id),
    ).fetchone()
    if row and row[0] == "committed":
        return json.loads(row[1])
    result = {"transaction_id": f"txn_{workflow_id}", "amount": amount}
    conn.execute(
        "INSERT OR REPLACE INTO operation (workflow_id, operation_id, idempotency_key, status, result) "
        "VALUES (?, ?, ?, 'committed', ?)",
        (workflow_id, operation_id, key, json.dumps(result)),
    )
    conn.commit()
    return result


STEP_ORDER = ["read_email", "generate_invoice", "process_payment", "send_confirmation", "complete"]


def run_pending(workflow_id: str) -> None:
    """Execute steps until done, saving checkpoints between them."""
    while True:
        state = load_state(workflow_id)
        remaining = [s for s in STEP_ORDER if s not in state["completed_steps"]]
        if not remaining:
            state["status"] = "completed"
            save_checkpoint(workflow_id, state)
            emit(workflow_id, "workflow_completed")
            return
        step = remaining[0]
        state["current_step"] = step
        state["status"] = "running"
        save_checkpoint(workflow_id, state)
        emit(workflow_id, "step_started", step_id=step)

        if step == "process_payment":
            emit(workflow_id, "external_call_start", step_id=step)
            charge(workflow_id, 99.00)
            emit(workflow_id, "external_call_completed", step_id=step)

        state["completed_steps"].append(step)
        state["step_outputs"][step] = {"ok": True}
        emit(workflow_id, "checkpoint_saving", step_id=step)
        save_checkpoint(workflow_id, state)
        emit(workflow_id, "checkpoint_saved", step_id=step)
        emit(workflow_id, "step_completed", step_id=step)


@app.get("/health")
def health():
    return {"protocol_version": PROTOCOL_VERSION, "agent_version": AGENT_VERSION, "status": "healthy"}


@app.post("/workflow/start")
def start(body: dict):
    workflow_id = f"wf_{abs(hash(json.dumps(body))) % 10**8:08d}"
    state = {
        "workflow_id": workflow_id,
        "status": "running",
        "current_step": STEP_ORDER[0],
        "completed_steps": [],
        "step_outputs": {},
    }
    conn.execute(
        "INSERT OR REPLACE INTO workflow (workflow_id, state, sequence) VALUES (?, ?, 0)",
        (workflow_id, base64.b64encode(json.dumps(state).encode()).decode()),
    )
    conn.commit()
    emit(workflow_id, "workflow_started")
    return {"protocol_version": PROTOCOL_VERSION, "workflow_id": workflow_id, "status": "running"}


@app.get("/workflow/{workflow_id}/state")
def state(workflow_id: str):
    s = load_state(workflow_id)
    return {"protocol_version": PROTOCOL_VERSION, **s}


@app.get("/workflow/{workflow_id}/checkpoint")
def checkpoint(workflow_id: str):
    row = conn.execute(
        "SELECT state, sequence FROM workflow WHERE workflow_id = ?", (workflow_id,)
    ).fetchone()
    if row is None:
        raise HTTPException(404, detail="workflow not found")
    return {
        "protocol_version": PROTOCOL_VERSION,
        "workflow_id": workflow_id,
        "checkpoint_id": f"cp_{workflow_id}_{row[1]}",
        "sequence": row[1],
        "created_at": now_iso8601(),
        "payload": {"format": "application/json", "data": row[0]},
    }


@app.get("/workflow/{workflow_id}/events")
def events(workflow_id: str):
    rows = conn.execute(
        "SELECT body FROM event WHERE workflow_id = ? ORDER BY sequence", (workflow_id,)
    ).fetchall()
    return {
        "protocol_version": PROTOCOL_VERSION,
        "workflow_id": workflow_id,
        "events": [json.loads(r[0]) for r in rows],
    }


@app.post("/workflow/{workflow_id}/resume")
def resume(workflow_id: str, body: dict):
    load_state(workflow_id)  # 404s if unknown; payload is optional when reusing local state
    run_pending(workflow_id)
    return {"protocol_version": PROTOCOL_VERSION, "status": "resumed", "workflow_id": workflow_id}
```

Run it:

```bash
pip install fastapi uvicorn
uvicorn adapter:app --port 8080
```

Then point `crash-test.yaml` at `http://localhost:8080` and iterate through the six windows.

---

## Adapter checklist

- [ ] All six endpoints implemented; every response carries `protocol_version`
- [ ] Errors are RFC 7807 ProblemDetails with correct status codes
- [ ] `/health` answers only when the checkpoint store is ready
- [ ] `workflow_id` unique per test run
- [ ] Checkpoint saved after every durable transition and before every external call
- [ ] `checkpoint.sequence` monotonic across restarts
- [ ] Events append-only, strictly increasing, persisted, emitted before the work for `*_start`
- [ ] `/resume` resumes from the last durable step and is idempotent
- [ ] Stable `operation_id` and reused `idempotency_key` for every side effect
- [ ] Baseline run completes before enabling crash windows
- [ ] All six crash windows pass repeatedly, then run in CI

## See also

- [protocol.md](protocol.md) — normative endpoint specification
- [quickstart.md](quickstart.md) — install, configure, and run
- [examples/langgraph-agent](../examples/langgraph-agent/) — reference implementation with LangGraph + PostgreSQL checkpoints
