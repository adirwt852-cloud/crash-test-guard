# Crash-Test Guard

**Prove your AI agent survives a crash — before production finds out.**

A CI/CD reliability gate for stateful, multi-step AI agents. It starts your agent, crashes it at a specific point, restarts it, and verifies it recovers correctly — no duplicate payments, no lost steps, no corrupted checkpoints.

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![CI](https://img.shields.io/github/actions/workflow/status/adirwt852-cloud/crash-test-guard/ci.yml?branch=main&label=CI)](https://github.com/adirwt852-cloud/crash-test-guard/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/adirwt852-cloud/crash-test-guard?display_name=tag)](https://github.com/adirwt852-cloud/crash-test-guard/releases)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

---

## The Problem

Your agent is halfway through a business workflow when the container dies:

```
1. Read customer email        ✓
2. Generate invoice           ✓
3. Process payment            ✓  ← CRASH
4. Send confirmation email    ○
5. Complete task              ○
```

After restart:
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

## Quick Start

### 1. Install

```bash
# macOS / Linux
brew tap crash-test-guard/tap https://github.com/crash-test-guard/homebrew-crash-test-guard
brew install crash-test-guard

# Windows
scoop bucket add crash-test-guard https://github.com/crash-test-guard/scoop-crash-test-guard
scoop install crash-test-guard

# Or download from GitHub Releases
```

### 2. Add the protocol to your agent

Your agent (any language/framework) implements **6 HTTP endpoints**:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/health` | GET | Liveness + protocol version |
| `/workflow/start` | POST | Start workflow, return `workflow_id` |
| `/workflow/{id}/state` | GET | Current step, completed steps, outputs |
| `/workflow/{id}/checkpoint` | GET | Serializable checkpoint (opaque) |
| `/workflow/{id}/events` | GET | Event stream for crash timing |
| `/workflow/{id}/resume` | POST | Resume from checkpoint |

[Full protocol spec →](docs/protocol.md) | [Build an adapter →](docs/adapter-development.md)

**Reference implementation:** [`examples/langgraph-agent/`](examples/langgraph-agent/) (FastAPI + LangGraph + PostgreSQL)

### 3. Configure your crash test

`crash-test.yaml`:
```yaml
version: "1.0"

agent:
  command: ["python", "my_agent.py"]
  protocol: http
  endpoint: "http://localhost:8080"
  startup_timeout: "30s"
  shutdown_timeout: "10s"

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
      timing: after_event
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

[All config options →](configs/crash-test.example.yaml) | [6 crash windows →](configs/)

### 4. Run locally

```bash
crash-test-guard test --config crash-test.yaml
# Exit 0 = PASS, 1 = FAIL, 2 = ERROR
```

### 5. Add to CI/CD

**GitHub Actions:**
```yaml
- name: Crash test
  run: |
    crash-test-guard test --config crash-test.yaml --report-format junit
```

**GitLab CI:**
```yaml
crash_test:
  script:
    - crash-test-guard test --config crash-test.yaml --report-format junit
  artifacts:
    reports:
      junit: crash-test-report.xml
```

**Jenkins:**
```groovy
stage('Crash Test') {
  sh 'crash-test-guard test --config crash-test.yaml --report-format junit'
  junit 'crash-test-report.xml'
}
```

---

## Verification Checks

Every enabled check must pass:

| Check | What it validates |
|-------|-------------------|
| **Checkpoint Consistency** | Checkpoint is readable, same workflow, sequence never regresses |
| **Monotonic Progress** | No completed step is lost; execution only moves forward |
| **Duplicate Side Effects** | Each idempotency key observed exactly once across crash + resume |
| **Workflow Completion** | Every expected step runs, final state is `completed` |
| **Output Equivalence** | Final status matches a crash-free baseline run |

---

## Supported Crash Scenarios

| Scenario | What it does |
|----------|--------------|
| `process_kill` | `SIGKILL` the agent process — no cleanup, no flush |
| `process_term` | `SIGTERM` the agent process — graceful shutdown path |
| `container_restart` | `docker kill` then `docker start` — restart same container |
| `container_replace` | `docker rm -f` then fresh `docker run` — lose all ephemeral state |
| `pod_delete` | Delete Kubernetes pod — K8s recreates it (same spec) |
| `pod_replace` | Delete pod + fresh `kubectl run` — lose all ephemeral state |

Container scenarios apply when `agent.image` is set in config; Kubernetes scenarios apply when `agent.namespace`/`label_selector`/`pod_name` is set; otherwise the local executor manages the process directly.

---

## Documentation

| Document | Contents |
|----------|----------|
| [Quickstart](docs/quickstart.md) | Install, configure, first passing run in ~15 minutes |
| [Protocol](docs/protocol.md) | Complete agent protocol specification |
| [Adapter Development](docs/adapter-development.md) | How to implement the protocol for a new framework |
| [Example Configs](configs/) | Ready-to-use configs for 6 crash windows |

---

## Examples

| Example | Stack | Checkpoint Store |
|---------|-------|------------------|
| [`examples/langgraph-agent`](examples/langgraph-agent/) | [LangGraph](https://langchain-ai.github.io/langgraph/) | PostgreSQL |

The reference adapter shows durable checkpoints, event emission for precise crash timing, and an idempotency ledger. Use it as a template for your own agent.

---

## Installation Details

Prebuilt binaries for Linux, macOS, and Windows on amd64 and arm64.

| Platform | Download |
|----------|----------|
| Linux amd64 | `crash-test-guard_<version>_linux_amd64.tar.gz` |
| Linux arm64 | `crash-test-guard_<version>_linux_arm64.tar.gz` |
| macOS amd64 | `crash-test-guard_<version>_darwin_amd64.tar.gz` |
| macOS arm64 | `crash-test-guard_<version>_darwin_arm64.tar.gz` |
| Windows amd64 | `crash-test-guard_<version>_windows_amd64.zip` |

Verify with `checksums.txt` (SHA256).

---

## Contributing

Contributions welcome — new adapters, crash scenarios, and reporters especially.

1. Fork the repo and create a feature branch
2. Add tests for your change
3. Run `make lint test`
4. Open a pull request

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and guidelines.

---

## License

Apache-2.0 — see [LICENSE](LICENSE).

---

## Support

- **Questions & ideas:** [GitHub Discussions](https://github.com/adirwt852-cloud/crash-test-guard/discussions)
- **Bugs & feature requests:** [GitHub Issues](https://github.com/adirwt852-cloud/crash-test-guard/issues)
- **Security reports:** see [SECURITY.md](SECURITY.md)

---

**Ship agents that survive the crash, not agents that discover it in production.**
