# Quickstart

Go from install to a passing crash test in about 15 minutes. Crash-Test Guard starts your agent, kills it at a chosen point in the workflow, restarts it, and verifies that recovery did not repeat any side effects.

**Before you start:** an agent that runs a workflow and persists checkpoints. Any language works — the protocol is plain HTTP/JSON.

---

## 1. Install the binary (2 min)

The release binary is static and has no runtime dependencies.

```bash
# Linux (amd64)
curl -sSL https://github.com/crash-test-guard/crash-test-guard/releases/latest/download/crash-test-guard_linux_amd64 \
  -o crash-test-guard
chmod +x crash-test-guard

# macOS (arm64)
curl -sSL https://github.com/crash-test-guard/crash-test-guard/releases/latest/download/crash-test-guard_darwin_arm64 \
  -o crash-test-guard
chmod +x crash-test-guard
```

Verify the install:

```bash
./crash-test-guard version
```

Homebrew and Scoop installs are published with each release:

```bash
brew install crash-test-guard/tap/crash-test-guard
```

---

## 2. Add the protocol to your agent (5 min)

Expose five endpoints over HTTP/JSON. Every response includes `protocol_version`.

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/health` | GET | Report readiness and protocol version |
| `/workflow/start` | POST | Start a workflow, return `workflow_id` |
| `/workflow/{id}/state` | GET | Current status and completed steps |
| `/workflow/{id}/checkpoint` | GET | Opaque checkpoint envelope |
| `/workflow/{id}/resume` | POST | Resume from a checkpoint payload |

A sixth endpoint, `/workflow/{id}/events`, is required for event-triggered crash scenarios (recommended, and used by the examples). See [protocol.md](protocol.md) for exact schemas.

Minimum viable implementation (Python/FastAPI):

```python
import uuid
from fastapi import FastAPI

app = FastAPI()
workflows = {}

@app.get("/health")
def health():
    return {"protocol_version": "1.0", "agent_version": "0.1.0", "status": "healthy"}

@app.post("/workflow/start")
def start(body: dict):
    workflow_id = f"wf_{uuid.uuid4().hex[:8]}"
    workflows[workflow_id] = {
        "status": "running",
        "current_step": "read_email",
        "completed_steps": [],
        "step_outputs": {},
    }
    return {"protocol_version": "1.0", "workflow_id": workflow_id, "status": "running"}

@app.get("/workflow/{workflow_id}/state")
def state(workflow_id: str):
    wf = workflows[workflow_id]
    return {"protocol_version": "1.0", "workflow_id": workflow_id, **wf}

@app.get("/workflow/{workflow_id}/checkpoint")
def checkpoint(workflow_id: str):
    return {
        "protocol_version": "1.0",
        "workflow_id": workflow_id,
        "checkpoint_id": f"cp_{uuid.uuid4().hex[:8]}",
        "sequence": 1,
        "created_at": "2026-01-15T10:30:00Z",
        "payload": {"format": "application/json", "data": "e30="},
    }

@app.post("/workflow/{workflow_id}/resume")
def resume(workflow_id: str, body: dict):
    return {"protocol_version": "1.0", "status": "resumed", "workflow_id": workflow_id}
```

For a complete implementation with durable checkpoints, event emission, and an idempotent operation ledger, copy [examples/langgraph-agent](../examples/langgraph-agent/).

---

## 3. Create `crash-test.yaml` (2 min)

```yaml
version: "1.0"

agent:
  command: ["python", "my_agent.py"]
  protocol: http
  endpoint: "http://localhost:8080"
  startup_timeout: 30s
  shutdown_timeout: 10s

  # Docker executor (optional)
  image: "my-agent:latest"
  network: "host"
  volumes:
    - "./data:/data"
  env:
    - "DATABASE_URL=postgres://..."

  # Kubernetes executor (optional)
  namespace: "production"
  label_selector: "app=my-agent"
  pod_name: "my-agent-pod"
  service_account: "crash-test-guard"
  kubeconfig: "/path/to/kubeconfig"

crash_scenarios:
  - type: process_kill
    trigger:
      event: step_completed
      step_id: process_payment
      timing: after_event        # crash immediately after this event
    fallback:
      timeout: 30s
      on_missed: fail

verification:
  checkpoint_consistency: true
  duplicate_side_effects: true
  workflow_completion: true
  baseline_comparison: true

reporting:
  format: junit
  output: "crash-test-report.xml"

deployment:
  fail_on_error: true
```

Six crash windows are available by changing `trigger.event` and `timing`:

| Window | `event` | `timing` |
|--------|---------|----------|
| Before checkpoint | `checkpoint_saving` | `before_event` |
| After checkpoint | `checkpoint_saved` | `after_event` |
| Before external call | `external_call_start` | `before_event` |
| After external call | `external_call_completed` | `after_event` |
| Before result persistence | `result_saving` | `before_event` |
| During finalization | `workflow_completing` | `before_event` |

Ready-made configs live in [configs/](../crash-test-guard/configs/).

---

## 4. Run locally (3 min)

Use two terminals. Crash-Test Guard will start and kill the agent itself, so terminal 1 is only for watching logs.

```bash
# Terminal 1 — start your agent
python my_agent.py
```

```bash
# Terminal 2 — run the crash test
./crash-test-guard test --config crash-test.yaml
```

Expected output:

```
SCENARIO: process_kill after step_completed(process_payment)
├── Agent startup:           ✓ 1.2s
├── Baseline run:            ✓ completed
├── Workflow execution:      ✓ reached process_payment
├── Crash injection:         ✓ SIGKILL at sequence 6
├── Agent restart:           ✓ 0.9s
├── Checkpoint recovery:     ✓ resumed
├── Checkpoint consistency:  ✓ envelopes match
├── Side-effect check:       ✓ no duplicate operations
├── Workflow completion:     ✓ completed
└── RESULT:                  PASS
```

Exit codes:

| Code | Meaning | CI action |
|------|---------|-----------|
| `0` | PASS | Deploy |
| `1` | FAIL | Block deployment |
| `2` | ERROR (isolation or protocol failure) | Investigate, do not deploy |

---

## 5. Add to GitHub Actions (3 min)

```yaml
# .github/workflows/crash-test.yml
name: Crash-Test Guard
on: [push, pull_request]

jobs:
  crash-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Python
        uses: actions/setup-python@v5
        with:
          python-version: "3.11"

      - name: Install agent dependencies
        run: pip install -r requirements.txt

      - name: Install Crash-Test Guard
        run: |
          curl -sSL https://github.com/crash-test-guard/crash-test-guard/releases/latest/download/crash-test-guard_linux_amd64 -o crash-test-guard
          chmod +x crash-test-guard

      - name: Run crash tests
        run: ./crash-test-guard test --config crash-test.yaml

      - name: Upload report
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: crash-test-report
          path: crash-test-report.xml
```

JUnit output is picked up by GitHub Actions, GitLab CI, Jenkins, and CircleCI without extra configuration.

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `ERROR` at startup | Check `agent.endpoint` matches the port your agent binds and that `/health` returns 200 |
| `fallback.on_missed: fail` triggered | The agent never emitted the target event — confirm `/events` returns sequence numbers |
| Side-effect check fails | Use a stable `operation_id` and `idempotency_key` per external call |
| Checkpoint comparison fails | Ensure checkpoint envelopes are stable; do not embed timestamps in compared metadata |

## Next steps

- [protocol.md](protocol.md) — full endpoint and error specification
- [adapter-development.md](adapter-development.md) — build a production-grade adapter
