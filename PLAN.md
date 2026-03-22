# PLAN.md — Implementation Plan

## Strategy

Build in **vertical slices** — each phase delivers a working, end-to-end feature. Avoid horizontal layers that don't produce runnable results.

Infrastructure is managed with **Terraform** (modular, environment-separated). Agent Engine is written in **Go**. LLM integration uses the **Claude API directly** (not Claude Code CLI).

## Terraform Module Structure

```
terraform/
├── envs/
│   ├── dev/
│   └── prod/
├── modules/
│   ├── network/          # VPC, private subnets, NAT, security groups
│   ├── ecr/              # Container image repository
│   ├── ecs/              # Cluster, task definition, log group, IAM roles
│   ├── sqs/              # Job queue + DLQ, redrive policy
│   ├── lambda_ingress/   # Discord interaction handler
│   ├── lambda_runner/    # SQS → ECS RunTask launcher
│   ├── dynamodb/         # Jobs table, GSI, TTL
│   ├── s3/               # Artifacts + outputs buckets, lifecycle
│   └── iam/              # Shared policies (or inline per module)
└── main.tf
```

Terraform implementation order (follows dependency graph):
1. network → 2. s3 → 3. dynamodb → 4. sqs → 5. ecr → 6. ecs → 7. lambda_runner → 8. lambda_ingress

## Phases

### Phase 0 — Prerequisites

- [x] AWS CLI login & credentials configured
- [x] Terraform initialized (backend, provider)
- [x] Discord Developer Portal: create application, add bot, get public key
- [x] Register slash command (`/nemuri`) via Discord API

### Phase 1 — Discord → Lambda (Ingress)

**Goal**: Receive a slash command and respond.

- [x] Deploy API Gateway + Lambda (Ingress) via Terraform
- [x] Implement signature verification (Discord public key)
- [x] Handle PING (type=1) → return PONG
- [x] Handle slash command (type=2) → return deferred ACK (type=5)
- [x] Set Interaction Endpoint URL in Discord Developer Portal
- [x] Verify end-to-end: slash command in Discord → "Bot is thinking..." displayed

### Phase 2 — SQS Integration

**Goal**: Ingress Lambda enqueues jobs.

- [x] Deploy SQS queue + DLQ via Terraform
- [x] Ingress Lambda: generate job_id, send SQS message `{ job_id, prompt, interaction_token, channel_id, application_id }`
- [x] Verify: message appears in SQS after slash command

### Phase 3 — ECS RunTask

**Goal**: SQS triggers ECS task execution.

- [x] Deploy ECS cluster + task definition via Terraform (initial container: `echo "hello from ECS"`)
- [x] Deploy Runner Lambda (SQS trigger → `ecs:RunTask`)
- [x] Build and push container image to ECR
- [x] Verify: slash command → SQS → ECS task runs → CloudWatch log shows "hello"

### Phase 4 — DynamoDB State Management

**Goal**: Jobs are tracked with state.

- [x] Deploy DynamoDB jobs table via Terraform (PK: job_id, GSI: thread_id)
- [x] Ingress Lambda: create job record (state=INIT)
- [x] ECS container: read job, update state to RUNNING, then DONE on exit
- [x] Implement conditional write for locking (worker_id, heartbeat_at)
- [x] Implement heartbeat goroutine (update every 3 minutes)
- [x] Implement SQS visibility timeout extension (every 3 minutes)
- [x] Verify: job lifecycle visible in DynamoDB

### Phase 5 — Claude API Integration

**Goal**: Agent Engine calls Claude and returns results.

- [x] Implement LLM Adapter Layer (interface for swappable LLM providers)
- [x] Implement Claude API client in Go
- [x] Simple flow: receive prompt → call Claude → return text result
- [x] Send result back to Discord via interaction token (follow-up message)
- [x] Verify: slash command → Claude generates response → appears in Discord

### Phase 6 — GitHub & S3 Deliverables

**Goal**: Agent can create code and push to GitHub.

- [x] Configure Fine-grained PAT, store in Secrets Manager
- [x] Implement Tool Executor: commit, push, create PR via GitHub API
- [x] Implement S3 upload for non-code deliverables
- [x] Implement presigned URL generation for Discord delivery
- [x] Verify: slash command → code generated → PR created → link posted to Discord

### Phase 7 — Review Loop

**Goal**: Output is reviewed and improved before delivery.

- [x] Implement Reviewer function (single model, structured JSON output via tool_use)
- [x] Implement Rewriter function (partial regeneration of flagged issues only)
- [x] Implement review loop with convergence detection and max_revision limit
- [x] Verify: generated code is reviewed, issues fixed, then PR created

### Phase 8 — User Interaction (Questions & Approval)

**Goal**: Agent can ask questions and wait for user input.

- [x] Implement WAITING_USER_INPUT state transition
- [x] ECS posts question to Discord thread → saves state → exits
- [x] Ingress Lambda: detect thread_id → look up job → enqueue resume message
- [x] New ECS task resumes from saved state
- [x] Implement WAITING_APPROVAL for PR merge
- [x] Verify: agent asks question → user answers → agent resumes

### Post-MVP — Refactoring & Quality

**Goal**: Improve code quality, testability, and maintainability.

- [x] Extract job execution logic into `internal/executor/` package
- [x] Introduce `github.API` interface for testability
- [x] Add `llm.RoleUser` / `llm.RoleAssistant` constants (replace magic strings)
- [x] Remove unused `READY_FOR_PR` state and `step` field
- [x] Add `baseURL` field to GitHub/Discord clients for test server injection
- [x] Improve error messages with context wrapping across GitHub client
- [x] Add YAML frontmatter stripping to Markdown → PDF converter
- [x] Add Markdown output format instruction to Gathering phase prompt
- [x] Add tests: Discord client, GitHub client, executor, converter

### Phase 9 — Evaluation Framework

**Goal**: Quantitatively measure agent output quality so that changes (prompt improvements, review logic, model swaps) can be validated as improvements or regressions.

**Approach**: Local execution of Agent Engine with real Claude API calls and mocked GitHub API (fixture-based). Each test case runs N trials; results are evaluated with deterministic checks (expectations) and graduated scoring (rubrics).

**Key design decisions**:
- **Method B (local Agent Engine)**: All planned improvements (multi-model review, file tree pre-filtering, gathering limit judgment) are Agent Engine-internal changes, so end-to-end AWS testing is unnecessary for quality measurement.
- **Two-layer metrics**: `pass_rate` (all-or-nothing per trial, for regression detection — expected near 1.0) and `quality_score` (weighted rubric scoring, 0.0–1.0, for measuring improvement).
- **Immutable test cases**: Prompts and expectations cannot be changed after creation (only added). This preserves comparability with past baselines. Each test case has a `version` field as a safety valve.
- **raw_response preservation**: Every trial saves the full AgentResponse JSON, enabling retroactive analysis with new rubric criteria without re-running.
- **Fixture snapshots in S3**: Repository snapshots are stored in S3 (not git) to avoid repo bloat. Snapshots are versioned and shared across test cases. Managed via `eval sync` / `eval snapshot` CLI commands.

- [x] Implement eval CLI framework (`eval/cmd/eval/main.go`)
- [x] Implement GitHub API mock from fixture snapshots (`eval/fixture/`)
- [x] Implement test runner (`eval/runner/`)
- [x] Implement expectation checker (`eval/checker/`)
- [x] Implement rubric scorer (`eval/checker/`)
- [x] Implement result recorder with JSON I/O (`eval/recorder/`)
- [x] Create initial repo snapshot (`nemuri-v1`)
- [x] Create 12 golden test cases (`eval/testcases/case-001..012.json`)
- [x] Implement `compare` subcommand for before/after analysis
- [x] Implement `recheck` subcommand for retroactive rubric evaluation
- [x] Upload snapshot to S3
- [x] Run baseline evaluation and record results

## Future Phases (Post-MVP)

- Multi-model review (different models for security vs. style): use a different model for review to reduce self-evaluation bias
- Long-term memory: user profile, project snapshots (S3-stored summaries)
- On-demand embedding with FAISS for project context
- Hybrid execution mode (short session for interactive Q&A)
- Team support (multi-tenant, per-user IAM, version-based optimistic locking)
- Agent persona configuration (tone, coding principles, forbidden actions)
- File tree pre-filtering: lightweight Claude API call before gathering to identify relevant files from tree only, reducing unnecessary file reads
- Gathering limit judgment: when approaching iteration limit, explicitly ask Claude whether implementation is possible with current information (proceed to generating or trigger ask_user_question)
