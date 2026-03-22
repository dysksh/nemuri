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
- SQS provides decoupling and retry for ECS RunTask API failures (ECS task-level failures are not retried via SQS; see "SQS Message Lifecycle" section)

### State Management: Self-built (No Framework)

**Decision**: Build state management in-house using DynamoDB

**Rationale**:
- Agent Engine is essentially a "smart batch application"
- State machine is simple (6 states, fixed transitions)
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

### User Interaction: Thread-based Resume via Slash Commands

**Decision**: User responds to agent questions via `/nemuri <answer>` in Discord threads (not @mention)

**Rationale**:
- `@mention` requires Discord Gateway WebSocket (always-on process, conflicts with serverless)
- Slash commands in threads provide the same UX with existing infrastructure
- `channel_id` in thread interactions equals the thread ID, enabling job lookup via GSI

**Flow**:
1. Agent calls `ask_user_question` tool → ECS saves conversation context to S3
2. Creates Discord thread with question, transitions to `WAITING_USER_INPUT`, exits
3. User types `/nemuri <answer>` in the thread
4. Ingress Lambda queries GSI `thread_id-index` → finds waiting job
5. Saves `user_response` to DynamoDB, enqueues resume message to SQS
6. Runner Lambda starts new ECS task; ECS acquires lock, loads context from S3, appends user answer as tool_result, resumes agent loop

**Conversation context persistence**: Full LLM message history serialized as JSON in S3 (`artifacts/{job_id}/conversation_context.json`). Includes pending tool_use ID for constructing the tool_result on resume.

**WAITING_APPROVAL**: After PR creation, agent creates thread with approval instructions. User types `/nemuri approve` → Ingress Lambda transitions `WAITING_APPROVAL → DONE` directly (no ECS task needed).

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

### State Simplification: READY_FOR_PR and step Removed

**Decision**: Remove `READY_FOR_PR` state and `step` field from DynamoDB Job schema

**Rationale**:
- `READY_FOR_PR` was an intermediate state between RUNNING and WAITING_APPROVAL, but PR creation and state transition happen atomically in the executor — there is no window where the job is "ready for PR" but the PR hasn't been created
- RUNNING → WAITING_APPROVAL is a direct transition; the intermediate state added no value
- The `step` field (planning, building, review_loop, pr_creation) tracked execution phases but was never used for resume or retry logic. Phase tracking is internal to the agent loop (gathering/generating) and does not need to be persisted in DynamoDB

### Executor Extraction

**Decision**: Extract job execution logic from `cmd/agent-engine/main.go` into `internal/executor/` package

**Rationale**:
- `main.go` had grown to ~700 lines with mixed concerns (ECS lifecycle + job execution + Discord/GitHub/S3 delivery)
- The executor package handles: job execution orchestration, code/file/text delivery, conversation resume, Discord thread/notification management
- `main.go` is now focused on ECS lifecycle (env parsing, heartbeat, state transitions)
- Enables testability: `github.API` interface allows mocking in executor tests

### PDF Frontmatter Handling

**Decision**: Strip YAML frontmatter from Markdown before HTML/PDF conversion

**Rationale**:
- The LLM sometimes generates Markdown with YAML frontmatter (`---` delimited metadata block at the top)
- goldmark does not parse frontmatter; it renders the `---` delimiters as `<hr>` tags and the metadata as plain text or setext headings
- Stripping frontmatter before conversion ensures clean PDF output regardless of LLM behavior
- Lightweight string-based approach avoids adding a goldmark extension dependency

## Concurrency & Reliability Patterns

### SQS Message Lifecycle

- SQS sits between Ingress Lambda and Runner Lambda as a decoupling layer
- Runner Lambda is triggered via SQS event source mapping (batch_size=1)
- When Runner Lambda succeeds (RunTask accepted), the event source mapping **automatically deletes** the SQS message
- This means ECS tasks do not interact with SQS — message deletion and visibility management are handled entirely by the Lambda service
- SQS retry (via visibility timeout + redelivery) only covers Runner Lambda failures (e.g., RunTask API errors, ECS capacity issues)
- ECS task failures (job errors, AcquireLock failures, panics) are **not retried via SQS** — the message is already deleted by the time the ECS task runs
- DLQ receives messages after maxReceiveCount (3) Runner Lambda failures
- SQS also serves as a buffer between Ingress Lambda and ECS, ensuring Ingress Lambda can return Discord's deferred ACK within 3 seconds (SQS SendMessage ~20-50ms vs ECS RunTask ~500ms-2s)

### Idempotency

- SQS is at-least-once delivery; duplicate processing is possible
- Guard: check DynamoDB state before executing each step
- Guard: skip step if already completed (based on state and `revision`)
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
- DynamoDB records current state and `revision` for resume

## S3 Design Philosophy

| Store | Purpose | Example |
|---|---|---|
| DynamoDB | State & metadata | job status, step, revision |
| S3 artifacts/ | Execution history & debug | logs, review JSONs, intermediate code |
| S3 outputs/ | Final deliverables | PDF reports, ZIP archives |
| GitHub | Code deliverables | Feature branches, PRs |

Do not mix these concerns.

## Network Isolation: ECS Outbound Restriction

**Decision**: ECS tasks run in a public subnet with a security group that restricts outbound traffic to HTTPS (port 443) only

**Rationale**:
- Domain-level filtering (Squid proxy) adds ~$34/mo always-on cost and operational complexity (EC2 management, VPC Interface Endpoints)
- For a personal-use agent, the cost and complexity of domain-level filtering outweigh the security benefit
- Port 443 restriction blocks non-HTTPS exfiltration channels (DNS tunneling excluded, but low risk for this threat model)
- The agent's tool executor controls which external APIs are called — the LLM cannot make arbitrary HTTP calls directly

**Architecture**:

```
ECS Task (public subnet, SG: outbound 443 only)
  → IGW → External APIs (Claude, GitHub, Discord, AWS)
```

- ECS tasks run in a public subnet with a public IP (Fargate `assignPublicIp = ENABLED`)
- Security group outbound rules allow only TCP port 443 (HTTPS)
- No VPC Interface Endpoints needed (direct internet access via IGW)
- No Squid proxy, no NAT Gateway
- AWS services (S3, DynamoDB, Secrets Manager, ECR, CloudWatch Logs) all accessible over HTTPS (port 443)

**Trade-offs accepted**:
- HTTPS to arbitrary domains is possible (the LLM could theoretically be prompt-injected to call an attacker-controlled HTTPS endpoint via tool use)
- Mitigation: tool executor has a fixed set of allowed external services — no generic HTTP client tool is exposed to the LLM
- If stricter isolation becomes necessary (e.g., team use), revisit domain-level filtering — options include an `http.RoundTripper` allowlist wrapper in Go (zero infra cost) or Squid / Network Firewall at the infrastructure level

**Alternatives considered**:

| Approach | Monthly cost | Domain filtering | Decision |
|---|---|---|---|
| **Port 443 restriction only** | **$0** | **No** | **Adopted: sufficient for personal use** |
| + Squid (no NAT) + IF Endpoints | $34 ($3 Squid + $31 IF Endpoints) | Yes | Rejected: cost/complexity disproportionate to threat model |
| + Network Firewall | $577 (2 AZ) | Yes | Rejected: excessive cost |
| + NAT Gateway + Squid | $79 ($45 NAT + $3 Squid + $31 IF Endpoints) | Yes | Rejected: NAT Gateway cost |

## Cost Estimates (Personal Use)

| Component | Estimated Monthly Cost |
|---|---|
| AWS (Lambda, SQS, DynamoDB, S3, ECS) | 1,000–3,000 JPY |
| LLM API calls | 5,000–15,000 JPY |
| **Total** | **~6,000–18,000 JPY** |

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

## Development Container Isolation: dev / claude Separation

**Decision**: Separate the development container (dev) and the Claude Code container (claude)

**Rationale**:
- The dev container volume-mounts host dotfiles (`.aws/`, `.ssh/`, `.gitconfig`, etc.)
- Claude Code sends file contents to the LLM as context, creating a risk of credentials being read unintentionally
- Container-level isolation provides a stronger security boundary than software-level access control (e.g., `.claude/settings.json` deny rules)
- If a vulnerability is discovered in Claude Code itself, the blast radius is limited to the workspace

**Container layout**:

| Container | Purpose | Mounts | Claude Code |
|---|---|---|---|
| dev | Development (editor, git, AWS CLI, etc.) | workspace + dotfiles (`.aws`, `.ssh`, `.gitconfig`, nvim config, etc.) | No |
| claude | Claude Code execution only | workspace + `.claude` (auth only) | Yes |

**Dockerfile multi-stage structure**:
- `base`: Shared toolchain (Go, Terraform, linters, AWS CLI, Docker CLI, etc.)
- `dev`: base + Neovim
- `claude`: base + Node.js + Claude Code

Go module cache is intentionally separated into a dedicated volume (`claude-gomodcache`). This prevents operations in the claude container from affecting the dev container's cache.

**Claude container design principles**:
- No git commands (PR creation etc. is done in the dev container)
- Go toolchain (`go build` / `go test`) is available
- Entrypoint is `sleep infinity` (no initialization script needed, unlike dev)

## Evaluation Framework Design Decisions

### Why Local Agent Engine Execution (Method B), Not End-to-End AWS (Method A)

**Decision**: Evaluate agent output quality by running Agent Engine locally with real Claude API + mocked GitHub API, not through the full Discord→SQS→ECS pipeline.

**Rationale**:
- All planned quality improvements (prompt tuning, multi-model review, file tree pre-filtering, gathering limit judgment) are Agent Engine-internal changes
- The Discord→SQS→ECS path is infrastructure plumbing that doesn't affect output quality
- Method B is cheaper (API cost only), faster, and more reproducible
- Method A would be needed only for availability/reliability testing, which is a separate concern tracked via CloudWatch

### Two-Layer Metrics: pass_rate + quality_score

**Decision**: Use `pass_rate` (binary, all-or-nothing per trial) for regression detection and `quality_score` (weighted rubric, 0.0–1.0) for quality measurement.

**Rationale**:
- Initial expectations (structural checks like response_type, file_present, content_contains) are intentionally lenient — the current system already passes most of them, so pass_rate ≈ 1.0
- A pass_rate of 1.0 cannot measure improvement, only regression
- Rubrics with strong/weak/none matching and weighted scoring provide a continuous metric (expected baseline: 0.5–0.8) with room to measure improvement
- Both metrics are recorded per trial; pass_rate catches catastrophic failures while quality_score tracks incremental progress

### Immutable Test Cases with raw_response Preservation

**Decision**: Test case prompts and expectations are immutable after creation. Every trial saves the full AgentResponse JSON.

**Rationale**:
- Changing a test case invalidates all past baselines for that case — before/after comparison becomes impossible
- The `version` field exists as a safety valve for critical corrections, with the understanding that different versions are not comparable
- Saving raw_response enables retroactive analysis: new rubric criteria can be applied to past results without re-running (which would cost API calls and introduce temporal variation)
- Expectations can only be added (new test cases), never modified or deleted

### Fixture Snapshots in S3 (Not Git)

**Decision**: Store repository snapshots for test fixtures in S3, not in git.

**Rationale**:
- A full repo snapshot (~1–5 MB) duplicated per test case would bloat git history permanently (git never forgets large blobs even if deleted later)
- S3 provides versioned, durable storage at negligible cost
- `snapshots.json` (the index) is git-tracked and contains S3 keys + SHA256 checksums for integrity verification
- Snapshots are shared across test cases (e.g., case-001, 002, 003 all reference `nemuri-v1`) to avoid duplication
- New snapshots are created infrequently (when the codebase changes significantly enough to warrant new test cases against the new state)

### Question Handling in Evaluation

**Decision**: All test cases have a default question answer ("あなたの判断に任せます。最善と思われる方法で進めてください。"). High-ambiguity cases have case-specific answers.

**Rationale**:
- LLM behavior is non-deterministic — even low-ambiguity prompts may trigger ask_user_question, which would halt the test without an answer
- A generic default answer prevents test hangs without biasing the output toward a specific implementation
- Case-specific answers for high-ambiguity prompts (e.g., case-008 "REST APIを作って") narrow the scope enough to make expectations meaningful, simulating the real user interaction pattern
- The fixed answer is returned regardless of the question content — this is acceptable because the goal is reproducible quality measurement, not natural conversation simulation

### Eval Directory Placement (In-Repo, Not Separate)

**Decision**: Place the evaluation framework in `eval/` within this repository, not in a separate repository.

**Rationale**:
- The eval CLI must import `internal/agent`, `internal/llm`, and other `internal/` packages to call `Agent.RunWithReview()` directly
- Go's `internal` package restriction prevents cross-repository imports — a separate repo would require duplicating types or adding HTTP indirection
- The dependency direction is enforced by convention: `eval/` → `internal/` is allowed, `internal/` → `eval/` is forbidden
- The `eval/` directory does not affect production builds (not referenced by `cmd/` or `Dockerfile`)

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
