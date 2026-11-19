<div align="center">

# Crash-Test Guard

**Prove your AI agent survives a crash — before production finds out.**

A CI/CD reliability gate for stateful, multi-step AI agents.

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![CI](https://img.shields.io/github/actions/workflow/status/adirwt852-cloud/crash-test-guard/ci.yml?branch=main&label=CI)](https://github.com/adirwt852-cloud/crash-test-guard/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/adirwt852-cloud/crash-test-guard?display_name=tag)](https://github.com/adirwt852-cloud/crash-test-guard/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/adirwt852-cloud/crash-test-guard)](https://goreportcard.com/report/github.com/adirwt852-cloud/crash-test-guard)
[![Coverage](https://img.shields.io/codecov/c/github/adirwt852-cloud/crash-test-guard)](https://codecov.io/gh/adirwt852-cloud/crash-test-guard)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

[Quickstart](docs/quickstart.md) · [Protocol](docs/protocol.md) · [Build an Adapter](docs/adapter-development.md) · [Contributing](CONTRIBUTING.md)

</div>

---

## The Problem

Your agent is halfway through a business workflow when the container dies:

```text
1. Read customer email        ✓
2. Generate invoice           ✓
3. Process payment            ✓  ← CRASH
4. Send confirmation email    ○
5. Complete task              ○
```

After restart, which of these is true?

- Does it resume at step 3, or replay from step 1?
- Does it charge the customer **twice**?
- Does it send a **duplicate** confirmation email?
- Is the checkpoint even readable?

Most teams find out in production. Crash-Test Guard finds out in CI.

---

## The Solution

A **single static binary** that drops into any CI pipeline. It:

1. **Starts your agent** — any language, any framework
2. **Crashes it deterministically** — at a specific event, step, or checkpoint boundary
3. **Restarts it and verifies recovery** — checkpoint integrity, monotonic progress, no duplicate side effects, successful completion
4. **Blocks the deploy on failure** — non-zero exit code, JUnit output for your CI dashboard

No SDK. No rewrite. Just six HTTP endpoints.

---

## How It Works

### 1. Speak the protocol

Any language. Any framework. Six HTTP endpoints:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/health` | `GET` | Liveness + protocol version negotiation |
| `/workflow/start` | `POST` | Start a workflow, return a `workflow_id` |
| `/workflow/{id}/state` | `GET` | Current status, current step, completed steps |
| `/workflow/{id}/checkpoint` | `GET` | Serializable checkpoint (sequence + payload) |
| `/workflow/{id}/events` | `GET` | Event stream used to time crash injection |
| `/workflow/{id}/resume` | `POST` | Resume execution from the latest checkpoint |

Full schemas and error semantics: **[docs/protocol.md](docs/protocol.md)**.

### 2. Describe the crash you care about

Scenarios are **trigger-based**, not sleep-based. The guard waits for a real workflow event and crashes at the exact boundary you specify:

```yaml
version: "1.0"

agent:
  command: ["python", "agent.py"]
  protocol: http
  endpoint: "http://localhost:8080"
  startup_timeout: "30s"
  shutdown_timeout: "10s"

crash_scenarios:
  - type: process_kill
    trigger:
      event: step_completed        # workflow event to wait for
      step_id: process_payment     # specific step
      timing: after_event          # before_event | after_event
    fallback:
      timeout: 30s                 # give up waiting after this
      on_missed: fail              # fail | skip | continue

verification:
  checkpoint_consistency: true
  duplicate_side_effects: true
  workflow_completion: true
  baseline_comparison: true

reporting:
  format: human                    # human | junit | json
  output: ""
  verbose: false

deployment:
  fail_on_error: true
```

### 3. Run it in CI

```bash
crash-test-guard test --config crash-test.yaml
```

| Exit code | Meaning | CI action |
|-----------|---------|-----------|
| `0` | PASS — agent recovered correctly | Deploy |
| `1` | FAIL — recovery invariants violated | Block deploy |
| `2` | ERROR — harness or config problem | Fix the pipeline |

---

## Installation

### Homebrew (macOS / Linux)

```bash
brew install crash-test-guard/tap/crash-test-guard
```

### Scoop (Windows)

```powershell
scoop bucket add crash-test-guard https://github.com/crash-test-guard/scoop-crash-test-guard
scoop install crash-test-guard
```

### Binary download

```bash
curl -sSL https://github.com/adirwt852-cloud/crash-test-guard/releases/latest/download/crash-test-guard_linux_amd64.tar.gz | tar xz
chmod +x crash-test-guard
```

Prebuilt archives are published for Linux, macOS, and Windows on amd64 and arm64.

---

## CLI Reference

| Command | Description |
|---------|-------------|
| `crash-test-guard test --config <file>` | Run the crash scenarios in a config file |
| `crash-test-guard test -c <file> --report-format junit\|json\|human` | Override the report format |
| `crash-test-guard test -c <file> -v` | Verbose output |
| `crash-test-guard version` | Print version information |
| `crash-test-guard completion [bash\|zsh\|fish\|powershell]` | Generate shell completion |

---

## Verification Checks

Every enabled check must pass for the scenario to pass.

| Check | What it validates |
|-------|-------------------|
| **Checkpoint Consistency** | The checkpoint is readable after recovery, belongs to the same workflow, and its sequence never regresses |
| **Monotonic Progress** | No completed step is lost; execution only moves forward |
| **Duplicate Side Effects** | Each idempotency key is observed exactly once across crash + resume |
| **Workflow Completion** | Every expected step runs and the final state is `completed` |
| **Output Equivalence** | Final status matches a crash-free baseline run |

---

## Supported Crash Scenarios

| Scenario | What it does |
|----------|--------------|
| `process_kill` | `SIGKILL` the agent process — no cleanup, no flush |
| `process_term` | `SIGTERM` the agent process — graceful shutdown path |
| `container_restart` | `docker kill` then `docker start` — restart the same container |
| `container_replace` | `docker rm -f` then a fresh `docker run` — lose all ephemeral state |

Container scenarios apply when `agent.image` is set in the config; otherwise the local executor manages the agent process directly.

---

## Examples

| Example | Stack | Checkpoint store |
|---------|-------|------------------|
| [`examples/langgraph-agent`](examples/langgraph-agent/) | [LangGraph](https://langchain-ai.github.io/langgraph/) | PostgreSQL |

The reference adapter shows durable checkpoints, event emission for precise crash timing, and an idempotency ledger. Use it as a template for your own agent: **[docs/adapter-development.md](docs/adapter-development.md)**.

A minimal end-to-end walkthrough lives in **[docs/quickstart.md](docs/quickstart.md)**.

---

## Documentation

| Document | Contents |
|----------|----------|
| [docs/quickstart.md](docs/quickstart.md) | Install, configure, and get a first passing run in ~15 minutes |
| [docs/protocol.md](docs/protocol.md) | Complete agent protocol specification |
| [docs/adapter-development.md](docs/adapter-development.md) | How to implement the protocol for a new framework |

---

## Contributing

Contributions are welcome — new adapters, crash scenarios, and reporters especially.

1. Fork the repo and create a feature branch
2. Add tests for your change
3. Run `make lint test`
4. Open a pull request

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for development setup and guidelines.

---

## License

Apache-2.0 — see [LICENSE](LICENSE).

---

## Support

- **Questions & ideas:** [GitHub Discussions](https://github.com/adirwt852-cloud/crash-test-guard/discussions)
- **Bugs & feature requests:** [GitHub Issues](https://github.com/adirwt852-cloud/crash-test-guard/issues)
- **Security reports:** see [SECURITY.md](SECURITY.md)

<div align="center">

**Ship agents that survive the crash, not agents that discover it in production.**

</div>
