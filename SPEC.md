# SPEC.md — System Specification

## Overview

**Nemuri** is a task automation agent system that receives natural-language requests via Discord, executes them using an LLM (Claude API), and delivers results through GitHub, S3, and Discord notifications.

It is designed as a **controlled semi-autonomous agent** — the LLM reasons and plans, but execution happens under strict infrastructure-level controls.

## Goals

- Automate repetitive development and research tasks via natural-language instructions
- Support diverse deliverables: code (PRs), documents (PDF/slides), research reports
- Ensure safety through sandboxed execution, state management, and review loops
- Keep operational costs low (on-demand execution, no always-on infrastructure)
- Enable mobile-first UX via Discord (issue tasks from anywhere, including smartphones)

## Architecture

```
User
  ↓ (Discord slash command)
Discord
  ↓ (HTTPS POST — Interaction Endpoint)
API Gateway
  ↓
Lambda (Ingress)
  ↓
  ├─ DynamoDB: Create job (state=INIT)
  └─ SQS: Enqueue job_id
       ↓
Lambda (Runner)
  ↓
ECS Fargate (RunTask — one task per job)
  ↓
Agent Engine (Go binary)
  ├─ Claude API (LLM reasoning)
  ├─ DynamoDB (state management)
  ├─ GitHub (code deliverables)
  ├─ S3 (artifacts + final outputs)
  └─ Discord API (notifications + questions)
```

### Key Architectural Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Execution model | 1 job = 1 ECS task | Simple, cost-effective, no memory leaks |
| Queue | SQS | Decouples ingress from execution, enables parallel jobs |
| State store | DynamoDB | Serverless, low cost, conditional writes for locking |
| LLM integration | Claude API (direct) | Better control, logging, and programmatic handling than CLI |
| Language | Go | Fast compilation, small binaries, goroutines, mature AWS SDK |
| IaC | Terraform | Reproducible, modular, environment-separated |
| Chat interface | Discord slash commands | Mobile-friendly, serverless-compatible (no Gateway WebSocket needed) |

## Components

### 1. Discord Bot (Slash Command)

- Registered via Discord Developer Portal
- Interaction Endpoint URL points to API Gateway
- Slash command: `/agent <natural language prompt>`
- Must respond with ACK (type=5, deferred) within 3 seconds

### 2. Lambda — Ingress

- Receives Discord Interaction events
- Verifies request signature (Discord public key)
- Handles PING (type=1) → responds with PONG
- Handles slash command (type=2):
  - Generates `job_id` (UUID)
  - Stores job in DynamoDB (state=INIT)
  - Saves `interaction_token`, `channel_id`, `application_id`
  - Sends message to SQS
  - Returns deferred ACK (type=5)
- Idempotency: deduplicates by `event_id`

### 3. Lambda — Runner

- Triggered by SQS
- Calls ECS RunTask with job_id as environment variable
- Lightweight — only launches ECS tasks

### 4. SQS — Job Queue

- Standard queue with DLQ (Dead Letter Queue)
- Initial visibility timeout: 5–10 minutes
- ECS extends visibility every 3 minutes via `ChangeMessageVisibility`
- Message payload: `{ "job_id": "uuid" }`
- `DeleteMessage` called only after successful completion

### 5. ECS Fargate — Worker

- One-shot task execution (no ECS Service, no long-running container)
- Container: distroless base + Go binary + ca-certificates
- On startup:
  1. Acquire lock via DynamoDB conditional write
  2. Load job state from DynamoDB
  3. Execute agent engine logic
  4. Update state, push results
  5. Delete SQS message
  6. Exit

### 6. Agent Engine (Go)

The core application running inside ECS. Responsibilities:

- **Planner**: Decompose user request into actionable steps
- **Builder**: Execute steps (call Claude API, generate code/files)
- **Reviewer**: Evaluate output quality (single model for MVP)
- **Rewriter**: Fix only flagged issues from review
- **Memory Manager**: Read/write job state and user profile
- **Tool Executor**: Interface with GitHub, S3, Discord APIs

#### Review Loop

```
Builder generates output
  ↓
Reviewer evaluates (structured JSON: security, correctness, maintainability scores)
  ↓
If score < threshold:
  Rewriter fixes flagged issues only
  revision++
  → back to Reviewer
If score >= threshold:
  → proceed to PR creation or output delivery
```

Safety: max_revision limit (e.g., 10) to prevent infinite loops.
Convergence detection: stop if same issue flagged 3 times or score improvement < 0.1 for 3 rounds.

### 7. DynamoDB — Jobs Table

**Table**: `jobs`
**Primary Key**: `job_id` (String)

```json
{
  "job_id": "uuid",
  "thread_id": "discord_thread_id",
  "channel_id": "discord_channel_id",
  "request_user_id": "discord_user_id",
  "interaction_token": "...",
  "application_id": "...",

  "state": "INIT | RUNNING | WAITING_USER_INPUT | READY_FOR_PR | WAITING_APPROVAL | DONE | FAILED",
  "step": "planning | building | review_loop | pr_creation",
  "revision": 0,

  "worker_id": "uuid",
  "heartbeat_at": 1700000000,
  "version": 0,

  "artifact_s3_prefix": "s3://bucket/artifacts/job_id/",
  "github_repo": "owner/repo",
  "github_branch": "feature/job-id",
  "pr_url": "",

  "error_message": "",

  "created_at": 1700000000,
  "updated_at": 1700000000,
  "ttl": 1735689600
}
```

**GSI**:
- `thread_id-index` (PK: `thread_id`) — look up job by Discord thread

#### State Transitions

```
INIT → RUNNING → WAITING_USER_INPUT → (user responds) → RUNNING
INIT → RUNNING → READY_FOR_PR → WAITING_APPROVAL → DONE
Any  → FAILED
```

Allowed transitions (enforced in code):

```
INIT              → [RUNNING]
RUNNING           → [WAITING_USER_INPUT, READY_FOR_PR, FAILED]
WAITING_USER_INPUT→ [RUNNING]
READY_FOR_PR      → [WAITING_APPROVAL]
WAITING_APPROVAL  → [DONE]
```

#### Locking & Concurrency

- **Startup lock** (conditional write):
  ```
  Condition: state IN (INIT, FAILED, WAITING_USER_INPUT)
             AND (attribute_not_exists(worker_id) OR heartbeat_at < :expired)
  Update:    state=RUNNING, worker_id=:wid, heartbeat_at=:now
  ```
- **Heartbeat** (every 3 minutes):
  ```
  Condition: worker_id = :wid
  Update:    heartbeat_at = :now
  ```
  (Does NOT increment version)
- **State change**:
  ```
  Condition: worker_id = :wid AND version = :v
  Update:    state=:new, version=:v+1
  ```
- **Completion**:
  ```
  Condition: worker_id = :wid
  Update:    state=DONE, REMOVE worker_id
  ```

### 8. S3 — Storage

Single bucket with prefix separation (MVP). Separate buckets for team use later.

```
s3://nemuri-storage/
  artifacts/{job_id}/         # Internal: intermediate files, logs, review results
    metadata.json
    logs/
    revisions/rev1/
    context/
  outputs/{job_id}/           # External: final deliverables (PDF, ZIP, etc.)
```

| Prefix | Purpose | Retention | Access |
|---|---|---|---|
| `artifacts/` | Intermediate/debug data, re-run context | 30-day TTL | ECS task role only |
| `outputs/` | Final deliverables for user | Long-term | Presigned URL for Discord delivery |

### 9. GitHub Integration

- Use GitHub App (not personal access tokens) for security
- Operations: create repos, push code, create PRs
- Code deliverables are pushed to feature branches (`feature/{job_id}`)
- PR merge requires human approval (via Discord notification + GitHub UI)

### 10. Discord Notifications

- Completion notifications sent via Discord Bot API (webhook-style POST)
- Questions from agent posted to the job's Discord thread
- PR links, review summaries, and presigned S3 URLs included in notifications
- Cost: effectively zero

## User Interaction Flow

### New Task

1. User creates a new Discord thread
2. User types `/agent <prompt>` (slash command)
3. Bot responds "thinking..." (deferred ACK)
4. Job executes asynchronously
5. Results delivered to the same thread

### Agent Asks a Question

1. Agent posts question to the Discord thread
2. Agent sets state=WAITING_USER_INPUT, exits ECS task
3. User responds with @mention in the thread
4. Lambda detects thread_id → finds job → enqueues to SQS
5. New ECS task resumes from saved state

### PR Approval

1. Agent creates PR, posts link to Discord thread
2. State=WAITING_APPROVAL
3. User reviews and merges PR on GitHub

## Security

- Agents interact with external services only through the Tool Executor layer
- IAM roles follow least-privilege principle
- No direct CLI access for the LLM
- Prompt injection mitigated by structured output parsing
- Secrets stored in AWS Secrets Manager (GitHub tokens, API keys)
- Non-secret config in SSM Parameter Store

## Cost Optimization

- ECS Fargate: on-demand only (no always-on containers)
- DynamoDB: on-demand pricing
- Lambda: pay-per-invocation
- S3 artifacts: 30-day lifecycle policy
- LLM costs: mix strong models (builder/security review) with lightweight models (style review)
- Estimated monthly cost (personal use): ~10,000–20,000 JPY (AWS ~1,000–3,000 + LLM ~5,000–15,000)
