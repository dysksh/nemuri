# KNOWLEDGE.md — Design Decisions & Background

## Project Context

- **Author**: Backend engineer (~5 years experience), familiar with Go and AWS
- **Primary use**: Personal task automation (may expand to team use later)
- **Motivation**: Automate repetitive tasks — research, code scaffolding, PR creation, document generation — via natural-language instructions from Discord

## Why This Architecture

### Why Not Existing Solutions?

| Alternative | Limitation |
|---|---|
| GitHub Copilot Agent | PC/GitHub UI required; code-only; no custom review loop |
| Gemini CLI + GitHub Actions | CLI-based; no mobile UX; no multi-format deliverables |
| Notion Custom Agents | Document-focused; limited external API integration |
| AutoGPT / OpenClaw | Hard to control; no built-in safety; overkill for task automation |
| LangChain / LangGraph | Good for prototyping but abstractions interfere with strict control, cost management, and AWS integration |

### Key Differentiators

1. **Mobile-first UX** — Discord as the interface allows task submission from smartphones
2. **Multi-format deliverables** — Code (PRs), documents (PDF/slides), research reports
3. **Controlled autonomy** — LLM reasons, but execution is sandboxed via Tool Executor
4. **Review loop** — Automated quality checks with partial regeneration
5. **Low cost** — On-demand execution, no always-on infrastructure

## Design Decisions Log

### LLM Integration: API vs CLI

**Decision**: Use Claude API directly (not Claude Code CLI)

**Rationale**:
- Better programmatic control over inputs/outputs
- Structured JSON output parsing
- Easier logging and debugging
- No dependency on CLI binary in container

### Language: Go

**Decision**: Go for Agent Engine

**Rationale**:
- Author is experienced with Go
- Goroutines for concurrent heartbeat/timeout management
- Single binary → small container image
- Mature AWS SDK
- Sufficient performance (I/O-bound workload; Rust's speed advantage irrelevant)

### Execution Model: 1 Job = 1 ECS Task

**Decision**: No long-running ECS Service; each job runs as a one-shot Fargate task

**Rationale**:
- Cost: pay only for active execution time
- No memory leak accumulation
- Natural scaling via SQS queue depth
- Simple failure recovery (task crash → SQS redelivers)

### State Management: Self-built (No Framework)

**Decision**: Build state management in-house using DynamoDB

**Rationale**:
- Agent Engine is essentially a "smart batch application"
- State machine is simple (7 states, fixed transitions)
- Frameworks (LangGraph, etc.) add abstraction overhead without clear benefit
- DynamoDB conditional writes provide sufficient concurrency control

### Discord: Slash Commands (Not Message Monitoring)

**Decision**: Use Discord Interaction (slash command) model

**Rationale**:
- Serverless-compatible (HTTP endpoint, no WebSocket)
- No need for always-on bot process
- Natural language input supported via command arguments
- 3-second ACK constraint handled by deferred response (type=5)

**Important**: `@mention`-based message monitoring requires Discord Gateway (WebSocket + always-on process), which conflicts with the serverless architecture. Slash commands achieve the same UX without this overhead.

### Review: Single Model First

**Decision**: Start with single-model review; add multi-model later

**Rationale**:
- Multi-model review adds cost and complexity
- Single-model review already catches most issues
- Can be expanded to multi-model in a future phase

### Version Field in DynamoDB

**Decision**: Include `version` field even though conditional write handles locking

**Rationale**:
- `worker_id` + conditional write = execution lock (who runs)
- `version` = update ordering integrity (prevents stale writes)
- `heartbeat_at` = liveness detection (separate concern)
- Three-layer defense; version is cheap to maintain and useful when multiple components update the same record

**Important**: Heartbeat updates do NOT increment version. Version is incremented only on meaningful state changes.

## Concurrency & Reliability Patterns

### SQS Visibility Timeout

- Set initial timeout to 5–10 minutes (short)
- ECS extends visibility every 3 minutes via `ChangeMessageVisibility`
- This handles variable-length jobs without fixed timeout problems
- `DeleteMessage` called only after successful completion

### Idempotency

- SQS is at-least-once delivery; duplicate processing is possible
- Guard: check DynamoDB state before executing each step
- Guard: skip step if already completed (based on `step` and `revision`)
- Guard: don't create PR if already exists

### Heartbeat & Recovery

- ECS writes `heartbeat_at` every 3 minutes
- If ECS crashes, heartbeat stops
- Next ECS task can acquire lock if `heartbeat_at` is expired
- Optional: monitoring Lambda detects stale heartbeats and marks jobs FAILED

### Artifact Persistence

- ECS local storage is ephemeral (destroyed on task exit)
- All intermediate results saved to S3 (`artifacts/` prefix)
- Code pushed to GitHub branch after each significant step
- DynamoDB records current `step` and `revision` for resume

## S3 Design Philosophy

| Store | Purpose | Example |
|---|---|---|
| DynamoDB | State & metadata | job status, step, revision |
| S3 artifacts/ | Execution history & debug | logs, review JSONs, intermediate code |
| S3 outputs/ | Final deliverables | PDF reports, ZIP archives |
| GitHub | Code deliverables | Feature branches, PRs |

Do not mix these concerns.

## Cost Estimates (Personal Use)

| Component | Estimated Monthly Cost |
|---|---|
| AWS (Lambda, SQS, DynamoDB, S3, ECS) | 1,000–3,000 JPY |
| LLM API calls | 5,000–15,000 JPY |
| **Total** | **~10,000–20,000 JPY** |

Optimization levers:
- Use lightweight models for style review (future)
- S3 artifacts lifecycle policy (30-day TTL)
- ECS Fargate Spot (for non-critical jobs)

## Container Design

- **Base**: `gcr.io/distroless/base-debian12`
- **Contents**: Go binary + ca-certificates (+ tzdata if needed)
- **No shell** — reduces attack surface
- **Workspace**: `/workspace/` (ephemeral, for Claude's working files)
- **User**: non-root
- **Size**: ~20–40 MB

## Glossary

| Term | Meaning |
|---|---|
| Agent Engine | The Go application that orchestrates LLM calls, state management, and tool execution |
| Tool Executor | The layer that interfaces with external services (GitHub, S3, Discord) |
| LLM Adapter | Abstraction layer for LLM API calls (enables provider swapping) |
| Job | A single user request, tracked from INIT to DONE |
| Revision | An iteration of the review-rewrite loop |
| Artifact | Intermediate file generated during job execution |
| Deliverable | Final output given to the user |
