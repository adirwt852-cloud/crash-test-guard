# Agent Protocol Specification

**Version:** 1.0  
**Protocol Version:** 1.0  
**Transport:** HTTP/1.1 + JSON  
**Base Path:** `/`

---

## Overview

The Agent Protocol is a language-agnostic HTTP/JSON interface that any AI agent must implement to be tested by Crash-Test Guard. The protocol defines 6 endpoints for workflow lifecycle management, state inspection, checkpointing, event streaming, and recovery.

**Design Principles:**
- **Framework-agnostic** — Works with LangGraph, AutoGen, CrewAI, or custom agents
- **Opaque checkpoints** — Crash-Test Guard never interprets checkpoint payload
- **Deterministic triggers** — Event sequence numbers enable precise crash injection
- **Versioned** — Protocol version negotiation via header and response field

---

## Protocol Versioning

All responses **must** include a `protocol_version` field:
```json
{"protocol_version": "1.0", "...": "..."}
```

Clients **should** send the `X-Crash-Test-Guard-Protocol-Version` header:
```
X-Crash-Test-Guard-Protocol-Version: 1.0
```

**Negotiation Rules:**
- Agents define `MIN_SUPPORTED_VERSION` and `MAX_SUPPORTED_VERSION`
- On `/health`, Crash-Test Guard negotiates the highest common version
- Reject with `426 Upgrade Required` if `agent_version < MIN` or `agent_version > MAX`

---

## Error Format (RFC 7807 ProblemDetails)

All error responses follow [RFC 7807](https://datatracker.ietf.org/doc/html/rfc7807):

```json
{
  "type": "https://crash-test-guard.dev/errors/checkpoint-not-found",
  "title": "Checkpoint Not Found",
  "status": 404,
  "detail": "No checkpoint exists for workflow wf_abc123",
  "instance": "/workflow/wf_abc123/checkpoint"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | Yes | URI identifying the error type |
| `title` | string | Yes | Human-readable summary |
| `status` | integer | Yes | HTTP status code |
| `detail` | string | No | Human-readable explanation |
| `instance` | string | No | URI reference to the specific occurrence |

**Common Error Types:**
- `https://crash-test-guard/errors/workflow-not-found` (404)
- `https://crash-test-guard/errors/checkpoint-not-found` (404)
- `https://crash-test-guard/errors/invalid-checkpoint` (400)
- `https://crash-test-guard/errors/protocol-version-mismatch` (426)
- `https://crash-test-guard/errors/agent-unavailable` (503)

---

## Endpoints

### 1. Health Check

**GET** `/health`

Returns agent health status and protocol version.

#### Request
```
GET /health HTTP/1.1
X-Crash-Test-Guard-Protocol-Version: 1.0
```

#### Response (200 OK)
```json
{
  "protocol_version": "1.0",
  "agent_version": "1.2.0",
  "status": "healthy"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `protocol_version` | string | Protocol version this agent implements |
| `agent_version` | string | Agent's semantic version |
| `status` | string | One of: `healthy`, `degraded`, `unhealthy` |

---

### 2. Start Workflow

**POST** `/workflow/start`

Initializes a new workflow execution and returns a `workflow_id`.

#### Request
```
POST /workflow/start HTTP/1.1
Content-Type: application/json
X-Crash-Test-Guard-Protocol-Version: 1.0

{
  "input": {
    "email_id": "msg_123",
    "customer_id": "cust_456"
  }
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `input` | object | Yes | Workflow input parameters (agent-defined schema) |

#### Response (200 OK)
```json
{
  "protocol_version": "1.0",
  "workflow_id": "wf_abc123",
  "status": "running"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `protocol_version` | string | Protocol version |
| `workflow_id` | string | Unique workflow identifier (opaque to Crash-Test Guard) |
| `status` | string | Always `running` on successful start |

#### Errors
- `400` — Invalid input schema
- `409` — Workflow already exists (if agent enforces uniqueness)
- `503` — Agent not ready

---

### 3. Get Workflow State

**GET** `/workflow/{id}/state`

Returns current execution state including completed steps and their outputs.

#### Request
```
GET /workflow/wf_abc123/state HTTP/1.1
X-Crash-Test-Guard-Protocol-Version: 1.0
```

#### Response (200 OK)
```json
{
  "protocol_version": "1.0",
  "workflow_id": "wf_abc123",
  "status": "running",
  "current_step": "process_payment",
  "completed_steps": ["read_email", "generate_invoice"],
  "step_outputs": {
    "read_email": {"subject": "Invoice Request", "from": "customer@example.com"},
    "generate_invoice": {"invoice_id": "inv_789", "amount": 99.00}
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `protocol_version` | string | Protocol version |
| `workflow_id` | string | Workflow identifier |
| `status` | string | One of: `running`, `completed`, `failed`, `paused` |
| `current_step` | string | ID of currently executing step |
| `completed_steps` | string[] | Ordered list of completed step IDs |
| `step_outputs` | object | Map of step_id → output (agent-defined) |

#### Errors
- `404` — Workflow not found
- `410` — Workflow completed/expired, state no longer available

---

### 4. Get Checkpoint

**GET** `/workflow/{id}/checkpoint`

Returns the latest checkpoint envelope with opaque payload and metadata.

#### Request
```
GET /workflow/wf_abc123/checkpoint HTTP/1.1
X-Crash-Test-Guard-Protocol-Version: 1.0
```

#### Response (200 OK)
```json
{
  "protocol_version": "1.0",
  "workflow_id": "wf_abc123",
  "checkpoint_id": "cp_456",
  "sequence": 12,
  "created_at": "2026-01-15T10:30:00Z",
  "payload": {
    "format": "application/octet-stream",
    "data": "YmFzZTY0X2VuY29kZWRfb3BhcXVlX2NoZWNrcG9pbnQ="
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `protocol_version` | string | Protocol version |
| `workflow_id` | string | Workflow identifier |
| `checkpoint_id` | string | Unique checkpoint identifier |
| `sequence` | int64 | Monotonically increasing sequence number |
| `created_at` | string | ISO 8601 timestamp |
| `payload.format` | string | MIME type of payload (e.g., `application/octet-stream`, `application/json`) |
| `payload.data` | string | Base64-encoded opaque checkpoint bytes |

**Critical:** The `payload` is **completely opaque** to Crash-Test Guard. Agents may use any serialization format (pickle, JSON, msgpack, custom binary). Crash-Test Guard only compares envelope metadata for consistency verification.

#### Errors
- `404` — Workflow or checkpoint not found
- `409` — Checkpoint not yet available (workflow still initializing)

---

### 5. Get Event Stream

**GET** `/workflow/{id}/events`

Returns the execution event stream with monotonically increasing sequence numbers. Used for deterministic crash trigger synchronization.

#### Request
```
GET /workflow/wf_abc123/events HTTP/1.1
X-Crash-Test-Guard-Protocol-Version: 1.0
```

#### Response (200 OK)
```json
{
  "protocol_version": "1.0",
  "workflow_id": "wf_abc123",
  "events": [
    {
      "sequence": 1,
      "type": "workflow_started",
      "step_id": "read_email",
      "timestamp": "2026-01-15T10:29:45Z",
      "data": {}
    },
    {
      "sequence": 2,
      "type": "step_started",
      "step_id": "read_email",
      "timestamp": "2026-01-15T10:29:45Z",
      "data": {}
    },
    {
      "sequence": 3,
      "type": "step_completed",
      "step_id": "read_email",
      "timestamp": "2026-01-15T10:29:46Z",
      "data": {"subject": "Invoice Request"}
    },
    {
      "sequence": 4,
      "type": "step_started",
      "step_id": "generate_invoice",
      "timestamp": "2026-01-15T10:29:46Z",
      "data": {}
    },
    {
      "sequence": 5,
      "type": "step_completed",
      "step_id": "generate_invoice",
      "timestamp": "2026-01-15T10:29:47Z",
      "data": {"invoice_id": "inv_789", "amount": 99.00}
    },
    {
      "sequence": 6,
      "type": "checkpoint_saved",
      "step_id": "generate_invoice",
      "timestamp": "2026-01-15T10:29:47Z",
      "data": {"checkpoint_id": "cp_456", "sequence": 5}
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `protocol_version` | string | Protocol version |
| `workflow_id` | string | Workflow identifier |
| `events` | Event[] | Array of events ordered by sequence |

**Event Object:**
| Field | Type | Description |
|-------|------|-------------|
| `sequence` | int64 | **Monotonically increasing** — used for trigger synchronization |
| `type` | string | Event type (see below) |
| `step_id` | string | Associated step identifier |
| `timestamp` | string | ISO 8601 timestamp |
| `data` | object | Event-specific payload |

**Standard Event Types:**
| Type | Description | Typical Trigger Use |
|------|-------------|---------------------|
| `workflow_started` | Workflow initialization | — |
| `step_started` | Step execution began | `before_event` |
| `step_completed` | Step finished successfully | `after_event` |
| `checkpoint_saving` | Checkpoint serialization started | `before_event` |
| `checkpoint_saved` | Checkpoint persisted | `after_event` |
| `external_call_start` | External API call initiated | `before_event` |
| `external_call_completed` | External API call returned | `after_event` |
| `result_saving` | Final result persistence started | `before_event` |
| `workflow_completing` | Workflow finalization began | `before_event` |
| `workflow_completed` | Workflow finished successfully | — |

**Sequence Number Requirements:**
- Must be **strictly monotonically increasing** (no gaps required, but no repeats)
- Must persist across crashes (survive agent restart)
- Used by Crash-Test Guard for **deterministic trigger synchronization** (not wall-clock timing)

#### Errors
- `404` — Workflow not found

---

### 6. Resume Workflow

**POST** `/workflow/{id}/resume`

Resumes a workflow from a checkpoint. The checkpoint payload is the base64-encoded data from the checkpoint envelope.

#### Request
```
POST /workflow/wf_abc123/resume HTTP/1.1
Content-Type: application/json
X-Crash-Test-Guard-Protocol-Version: 1.0

{
  "checkpoint": "YmFzZTY0X2VuY29kZWRfb3BhcXVlX2NoZWNrcG9pbnQ="
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `checkpoint` | string | Yes | Base64-encoded opaque checkpoint payload |

#### Response (200 OK)
```json
{
  "protocol_version": "1.0",
  "status": "resumed",
  "workflow_id": "wf_abc123"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `protocol_version` | string | Protocol version |
| `status` | string | Always `resumed` on success |
| `workflow_id` | string | Same workflow identifier |

#### Errors
- `400` — Invalid checkpoint format or corrupted data
- `404` — Workflow not found
- `409` — Workflow not in resumable state (e.g., already completed)
- `422` — Checkpoint incompatible with current agent version

---

## Event Sequence Numbers

### Purpose
Event sequence numbers enable **deterministic crash injection** without relying on wall-clock timing or polling races.

### How It Works
1. Crash-Test Guard polls `/events` (or uses long-poll/WebSocket)
2. When target event `sequence` is observed, Crash-Test Guard injects crash
3. Agent **must** ensure sequence numbers persist across restarts

### Implementation Requirements
| Requirement | Rationale |
|-------------|-----------|
| Monotonically increasing | Enables precise "after event N" triggers |
| Survives agent restart | Checkpoint must include current sequence |
| No gaps required | But no duplicates; sequence is authoritative |
| Visible before step executes | `step_started` event must emit before work begins |

### Pre-Event Barrier (v1.1)
For tighter synchronization, agents **may** implement a pre-event barrier:
1. Agent emits `step_started` (sequence N)
2. Agent blocks at barrier, waiting for external signal
3. Crash-Test Guard observes sequence N, sends `CRASH_NOW` via dedicated endpoint
4. Agent receives signal, proceeds or crashes

This eliminates polling races entirely. The barrier endpoint is **not** part of the core 6 endpoints — it's an optional extension.

---

## Implementation Notes

### Checkpoint Envelope (Opaque Payload)

The checkpoint envelope provides metadata without coupling to payload format:

```json
{
  "protocol_version": "1.0",
  "workflow_id": "wf_abc123",
  "checkpoint_id": "cp_456",
  "sequence": 12,
  "created_at": "2026-01-15T10:30:00Z",
  "payload": {
    "format": "application/octet-stream",
    "data": "base64_encoded_opaque_checkpoint"
  }
}
```

**Agent Responsibilities:**
- Serialize full execution state to bytes (any format)
- Base64-encode for transport
- Include `format` MIME type for future tooling
- Increment `sequence` on each checkpoint save

**Crash-Test Guard Responsibilities:**
- Store envelope metadata for comparison
- Never decode or interpret `payload.data`
- Compare envelopes pre/post crash (ignoring `created_at`, `checkpoint_id`)

### Exactly-Once Semantics

Crash-Test Guard verifies **committed business operations**, not just idempotency key usage.

**Ledger Entry Schema (provided by test environment):**
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

**Verification Rules:**
| Check | Description |
|-------|-------------|
| **Exactly Once** | Each `operation_id` appears with `status: committed` exactly once |
| **Result Consistency** | Committed results match (same `transaction_id`, amount) |
| **No Phantom Operations** | No operations in crash run that weren't in baseline |
| **Idempotency Key Reuse** | Same `idempotency_key` → same `result` |

**Key Distinction:** `attempt_count` (retries) ≠ `committed` (final). Only `committed` operations count toward duplication checks.

### Baseline Comparison Rules

Every crash test runs **two executions** of the same workflow:
1. **Baseline** — Normal run, no crashes
2. **Crash-Injected** — Run with deterministic crash

**Comparison Criteria (all must PASS):**
| Criterion | Check |
|-----------|-------|
| Business State Equivalence | Final committed operations identical (ledger comparison) |
| Workflow Output Equivalence | Final output matches (semantic diff) |
| Invariant Preservation | All recovery invariants hold (§7.4 of SPEC) |
| No Extra Side Effects | Crash run ledger ⊆ Baseline ledger |
| Completeness | Crash run completed all baseline steps |

**Explicit Ignore List for Semantic Diff:**
- Timestamps (`created_at`, `updated_at`, `timestamp`)
- UUIDs (`workflow_id`, `checkpoint_id`, `operation_id`)
- Sequence numbers (`sequence`, event sequence)
- Runtime IDs (container IDs, pod names, process IDs)
- Event `sequence` field

### Recovery Invariants (Enforced by Verifier)

| Invariant | Description |
|-----------|-------------|
| **No Lost Steps** | `completed_steps_post ⊇ completed_steps_pre` (superset) |
| **Monotonic Progress** | `current_step_post ≥ current_step_pre` |
| **No Duplicate Side Effects** | Idempotency keys used exactly once across crash+resume |
| **Output Equivalence** | Final output matches non-crashed baseline |
| **Checkpoint Readability** | Post-crash checkpoint deserializes without error |

### Protocol Version Compatibility

Agents **must** define version bounds:
```go
const (
    MIN_SUPPORTED_VERSION = "1.0"
    MAX_SUPPORTED_VERSION = "1.0"
)
```

**Negotiation Flow:**
1. Crash-Test Guard calls `/health`
2. Agent returns `protocol_version` and `agent_version`
3. Crash-Test Guard checks: `MIN_SUPPORTED_VERSION <= agent_version <= MAX_SUPPORTED_VERSION`
4. If mismatch → `426 Upgrade Required` with `ProblemDetails`
5. Use highest common version for subsequent calls

---

## Quick Reference

| Endpoint | Method | Request Body | Response Key Fields |
|----------|--------|--------------|---------------------|
| `/health` | GET | — | `protocol_version`, `agent_version`, `status` |
| `/workflow/start` | POST | `{"input": {...}}` | `workflow_id`, `status: "running"` |
| `/workflow/{id}/state` | GET | — | `status`, `current_step`, `completed_steps[]`, `step_outputs` |
| `/workflow/{id}/checkpoint` | GET | — | `checkpoint_id`, `sequence`, `payload{format,data}` |
| `/workflow/{id}/events` | GET | — | `events[{sequence,type,step_id,timestamp,data}]` |
| `/workflow/{id}/resume` | POST | `{"checkpoint": "..."}` | `status: "resumed"`, `workflow_id` |

---

## Changelog

| Version | Date | Changes |
|---------|------|---------|
| 1.0 | 2026-01-15 | Initial specification: 6 endpoints, opaque checkpoints, event sequences, RFC 7807 errors |
