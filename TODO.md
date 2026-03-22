# TODO.md — Task Tracker

## Phase 0: Prerequisites

- [x] Configure AWS CLI credentials
- [x] Initialize Terraform (backend, provider config)
- [x] Create Discord application in Developer Portal
- [x] Add bot to Discord application
- [x] Obtain Discord public key
- [x] Register `/nemuri` slash command via Discord API

## Phase 1: Discord → Lambda (Ingress)

- [x] Write Terraform module: `network` (VPC, subnets, NAT, SG)
- [x] Write Terraform module: `lambda_ingress` (API Gateway + Lambda)
- [x] Implement Lambda handler: signature verification
- [x] Implement Lambda handler: PING → PONG
- [x] Implement Lambda handler: slash command → deferred ACK (type=5)
- [x] Set Interaction Endpoint URL in Discord Developer Portal
- [x] Test: `/nemuri hello` → "Bot is thinking..." in Discord

## Phase 2: SQS Integration

- [x] Write Terraform module: `sqs` (job queue + DLQ)
- [x] Update Ingress Lambda: generate job_id (UUID)
- [x] Update Ingress Lambda: send SQS message
- [x] Test: slash command → message visible in SQS console

## Phase 3: ECS RunTask

- [x] Write Terraform module: `ecr`
- [x] Write Terraform module: `ecs` (cluster, task definition, IAM roles)
- [x] Write Terraform module: `lambda_runner` (SQS trigger → RunTask)
- [x] Create Dockerfile (debian:12-slim + Go binary)
- [x] Build and push image to ECR
- [x] Test: slash command → ECS task runs → "hello" in CloudWatch

## Phase 4: DynamoDB State Management

- [x] Write Terraform module: `dynamodb` (jobs table, GSI: thread_id)
- [x] Update Ingress Lambda: create job record (state=INIT)
- [x] Implement Agent Engine: DynamoDB lock acquisition (conditional write)
- [x] Implement Agent Engine: state transition logic (allowed transitions map)
- [x] Implement Agent Engine: heartbeat goroutine (3-min interval)
- [x] Implement Agent Engine: SQS visibility timeout extension (3-min interval)
- [x] Implement Agent Engine: update state to DONE + delete SQS message on completion
- [x] Test: full job lifecycle visible in DynamoDB

## Phase 5: Claude API Integration

- [x] Design LLM Adapter interface (for future model swapping)
- [x] Implement Claude API client in Go
- [x] Implement simple flow: prompt → Claude → text result
- [x] Implement Discord follow-up message (POST via interaction token)
- [x] Write Terraform module: `s3` (artifacts + outputs buckets)
- [x] Test: `/nemuri <question>` → Claude answers → response in Discord

## Phase 6: GitHub & S3 Deliverables

- [x] Configure Fine-grained PAT, store in Secrets Manager
- [x] Implement Tool Executor: commit, push via GitHub API
- [x] Implement Tool Executor: create PR via GitHub API
- [x] Implement Tool Executor: S3 upload + presigned URL generation
- [x] Post PR link / presigned URL to Discord
- [x] Test: `/nemuri build a Go REST API` → PR created → link in Discord

## Phase 7: Review Loop

- [x] Define review output schema (JSON: scores + issues list via tool_use)
- [x] Implement Reviewer function (structured evaluation via submit_review tool)
- [x] Implement Rewriter function (partial fix of flagged issues only)
- [x] Implement review loop controller (convergence detection, max_revision)
- [x] Test: generated code → reviewed → issues fixed → PR created

## Phase 8: User Interaction

- [x] Implement WAITING_USER_INPUT state and ECS graceful exit
- [x] Implement Discord question posting to job thread
- [x] Update Ingress Lambda: detect thread_id → look up job → enqueue resume
- [x] Implement ECS resume from saved state
- [x] Implement WAITING_APPROVAL state for PR merge flow
- [x] Test: agent asks question → user answers → agent resumes and completes

## Post-MVP: Refactoring & Quality

- [x] Extract job execution logic into `internal/executor/` package
- [x] Introduce `github.API` interface for testability
- [x] Add `llm.RoleUser` / `llm.RoleAssistant` constants
- [x] Remove unused `READY_FOR_PR` state and `step` field
- [x] Add `baseURL` field to GitHub/Discord clients for test injection
- [x] Improve error messages with context wrapping (GitHub client)
- [x] Add YAML frontmatter stripping to Markdown → PDF converter
- [x] Add Markdown output format instruction to Gathering phase prompt
- [x] Add tests: Discord client, GitHub client, executor, converter

## Phase 9: Evaluation Framework

- [x] Implement eval CLI framework (`eval/cmd/eval/main.go`: run, compare, recheck subcommands)
- [x] Implement GitHub API mock from fixture directory (`eval/fixture/github_mock.go`)
- [x] Implement test runner: load test cases → build mock → call Agent.RunWithReview → collect results (`eval/runner/`)
- [x] Implement question handling: auto-respond with fixed answers, respect max_questions (`eval/runner/`)
- [x] Implement expectation checker: response_type, file_present, content_contains, syntax_valid, etc. (`eval/checker/`)
- [x] Implement rubric scorer: strong/weak/none matching, weighted quality_score calculation (`eval/checker/`)
- [x] Implement result recorder: JSON write/read, per-case summary stats (mean/min/max/median) (`eval/recorder/`)
- [x] Define type definitions: TestCase, Expectation, Rubric, TrialResult, RunRecord (`eval/types/`)
- [x] Create initial repo snapshot (nemuri-v1 from current commit)
- [x] Create 12 golden test cases (case-001..012)
- [x] Implement `compare` subcommand: diff two run results, flag regressions
- [x] Implement `recheck` subcommand: re-evaluate past raw_responses with current expectations/rubrics
- [x] Upload snapshot to S3 (`make eval-sync-up`)
- [x] Run baseline evaluation and record results

## Future (Post-MVP)

- [ ] Multi-model review (security reviewer + style reviewer)
- [ ] User profile memory (DynamoDB: preferences, coding style, stack)
- [ ] Project snapshot memory (S3: repo structure summaries)
- [ ] On-demand FAISS embedding for project context
- [ ] Hybrid execution mode (short session for Q&A, one-shot for tasks)
- [ ] Agent persona configuration (YAML: tone, principles, forbidden actions)
- [ ] Team support (multi-tenant isolation, per-user permissions)
- [ ] Monitoring Lambda (detect stale heartbeats → mark jobs FAILED)
- [x] File tree pre-filtering (soft suggestions based on lightweight LLM call before gathering)
- [x] Gathering limit judgment (dynamic input token budget: 80k tokens)
- [x] Duplicate file read prevention (skip re-reading already-cached files)
