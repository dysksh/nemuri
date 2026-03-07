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

### GitHub Authentication: Fine-grained PAT

**Decision**: Use a Fine-grained Personal Access Token (not GitHub App)

**Rationale**:
- GitHub App installation tokens cannot create repositories under personal accounts (`POST /user/repos` returns 403)
- A fine-grained PAT covers all requirements: new repo creation, PR creation, and read/write to existing repos
- Eliminates JWT generation and installation token refresh logic, simplifying the codebase

**Required permissions (minimum)**:
- Administration: Write (repo creation)
- Contents: Write (file push)
- Pull Requests: Write (PR creation)
- Scope: All repositories (must include repos that don't exist yet)

**Alternatives considered**:

| Approach | Pros | Cons |
|---|---|---|
| GitHub App (installed on org) | Auto-rotating tokens, limited blast radius on leak | Requires org, cannot create personal repos |
| GitHub App + PAT hybrid | Flexible | Two auth systems, added complexity |
| Fine-grained PAT only | Simple, works for user and org repos | Manual rotation required |

**Leak risk**:
- Administration: Write also permits repo deletion and settings changes (cannot scope to creation only)
- Contents: Write permits code overwrite and force-push
- Mitigation: stored in Secrets Manager, accessible only via ECS task role (AWS recommended pattern)
- ECS Fargate has no SSH, ECS Exec disabled by default, runs as non-root — small attack surface

**Token rotation**:
- Fine-grained PATs expire after at most 1 year; no API for programmatic renewal
- A notification system (EventBridge + Lambda → Discord) will alert before expiry, managed in this repo's Terraform

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

- **Base**: `debian:12-slim` (changed from distroless; wkhtmltopdf requires glibc + shared libs)
- **Contents**: Go binary + ca-certificates + wkhtmltopdf + fonts (+ tzdata if needed)
- **Workspace**: `/workspace/` (ephemeral, for Claude's working files)
- **User**: non-root

### PDF Conversion: wkhtmltopdf (Technical Debt)

**Status**: wkhtmltopdf is archived and no longer maintained. No future security patches will be released.

**Current choice rationale**: Simple, single-binary tool with no runtime dependencies beyond shared libs. Sufficient for current scope.

**Migration candidates** (when needed):
- **WeasyPrint** — Python-based, actively maintained, good CSS support
- **Playwright/Puppeteer** — Chromium-based, most accurate rendering, heavier footprint
- **go-wkhtmltopdf alternatives** — e.g., `chromedp` (Go + headless Chrome)

**Trigger to migrate**: If a security vulnerability is discovered in wkhtmltopdf, or if rendering quality becomes insufficient.

**Build availability risk**: The .deb is downloaded from GitHub Releases at build time. If the release becomes unavailable, builds will break. Mitigations:
- Dockerfile uses a separate download stage so the .deb is cached in Docker layer cache
- If GitHub releases go down, host the .deb in S3 or a private registry and update the URL

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
