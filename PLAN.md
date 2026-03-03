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

- [ ] AWS CLI login & credentials configured
- [ ] Terraform initialized (backend, provider)
- [ ] Discord Developer Portal: create application, add bot, get public key
- [ ] Register slash command (`/agent`) via Discord API

### Phase 1 — Discord → Lambda (Ingress)

**Goal**: Receive a slash command and respond.

- [ ] Deploy API Gateway + Lambda (Ingress) via Terraform
- [ ] Implement signature verification (Discord public key)
- [ ] Handle PING (type=1) → return PONG
- [ ] Handle slash command (type=2) → return deferred ACK (type=5)
- [ ] Set Interaction Endpoint URL in Discord Developer Portal
- [ ] Verify end-to-end: slash command in Discord → "Bot is thinking..." displayed

### Phase 2 — SQS Integration

**Goal**: Ingress Lambda enqueues jobs.

- [ ] Deploy SQS queue + DLQ via Terraform
- [ ] Ingress Lambda: generate job_id, send SQS message `{ job_id, prompt, interaction_token, channel_id, application_id }`
- [ ] Verify: message appears in SQS after slash command

### Phase 3 — ECS RunTask

**Goal**: SQS triggers ECS task execution.

- [ ] Deploy ECS cluster + task definition via Terraform (initial container: `echo "hello from ECS"`)
- [ ] Deploy Runner Lambda (SQS trigger → `ecs:RunTask`)
- [ ] Build and push container image to ECR
- [ ] Verify: slash command → SQS → ECS task runs → CloudWatch log shows "hello"

### Phase 4 — DynamoDB State Management

**Goal**: Jobs are tracked with state.

- [ ] Deploy DynamoDB jobs table via Terraform (PK: job_id, GSI: thread_id)
- [ ] Ingress Lambda: create job record (state=INIT)
- [ ] ECS container: read job, update state to RUNNING, then DONE on exit
- [ ] Implement conditional write for locking (worker_id, heartbeat_at)
- [ ] Implement heartbeat goroutine (update every 3 minutes)
- [ ] Implement SQS visibility timeout extension (every 3 minutes)
- [ ] Verify: job lifecycle visible in DynamoDB

### Phase 5 — Claude API Integration

**Goal**: Agent Engine calls Claude and returns results.

- [ ] Implement LLM Adapter Layer (interface for swappable LLM providers)
- [ ] Implement Claude API client in Go
- [ ] Simple flow: receive prompt → call Claude → return text result
- [ ] Send result back to Discord via interaction token (follow-up message)
- [ ] Verify: slash command → Claude generates response → appears in Discord

### Phase 6 — GitHub & S3 Deliverables

**Goal**: Agent can create code and push to GitHub.

- [ ] Create GitHub App, configure permissions
- [ ] Implement Tool Executor: git clone, commit, push, create PR
- [ ] Implement S3 upload for non-code deliverables
- [ ] Implement presigned URL generation for Discord delivery
- [ ] Verify: slash command → code generated → PR created → link posted to Discord

### Phase 7 — Review Loop

**Goal**: Output is reviewed and improved before delivery.

- [ ] Implement Reviewer function (single model, structured JSON output)
- [ ] Implement Rewriter function (partial regeneration of flagged issues only)
- [ ] Implement review loop with convergence detection and max_revision limit
- [ ] Verify: generated code is reviewed, issues fixed, then PR created

### Phase 8 — User Interaction (Questions & Approval)

**Goal**: Agent can ask questions and wait for user input.

- [ ] Implement WAITING_USER_INPUT state transition
- [ ] ECS posts question to Discord thread → saves state → exits
- [ ] Ingress Lambda: detect thread_id → look up job → enqueue resume message
- [ ] New ECS task resumes from saved state
- [ ] Implement WAITING_APPROVAL for PR merge
- [ ] Verify: agent asks question → user answers → agent resumes

## Future Phases (Post-MVP)

- Multi-model review (different models for security vs. style)
- Long-term memory: user profile, project snapshots (S3-stored summaries)
- On-demand embedding with FAISS for project context
- Hybrid execution mode (short session for interactive Q&A)
- Team support (multi-tenant, per-user IAM, version-based optimistic locking)
- Agent persona configuration (tone, coding principles, forbidden actions)
