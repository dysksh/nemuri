# CLAUDE.md

## Project Overview

**Nemuri** is a task automation agent system. Users submit natural-language requests via Discord slash commands, which are executed by an LLM-powered agent running on AWS (ECS Fargate). Results are delivered as GitHub PRs, S3 files, or Discord messages.

See [SPEC.md](SPEC.md) for detailed architecture, [PLAN.md](PLAN.md) for implementation phases, [TODO.md](TODO.md) for the current task list, and [KNOWLEDGE.md](KNOWLEDGE.md) for design decisions and rationale.

## Project Status

Phase 8 (User Interaction) implemented. Phases 0–8 are complete. Next: verification testing and future phases.

## Tech Stack

- **Language**: Go
- **Infrastructure**: AWS (ECS Fargate, Lambda, SQS, DynamoDB, S3, API Gateway)
- **IaC**: Terraform (modular, env-separated: `terraform/envs/{dev,prod}`, `terraform/modules/*`)
- **LLM**: Claude API (direct, not CLI)
- **Interface**: Discord slash commands
- **Container**: debian:12-slim base image, single Go binary (wkhtmltopdf dependency requires glibc + shared libs)

## Architecture Summary

```
Discord → API Gateway → Lambda (Ingress) → SQS → Lambda (Runner) → ECS Fargate
ECS runs Agent Engine (Go) → Claude API + GitHub + S3 + DynamoDB + Discord API
```

- 1 job = 1 ECS task (one-shot, no long-running service)
- State managed in DynamoDB with conditional writes and heartbeat
- Artifacts stored in S3; code pushed to GitHub

## Key Conventions

- **State transitions** are enforced via an allowed-transitions map in code
- **Locking** uses DynamoDB conditional writes (`worker_id` + `heartbeat_at`)
- **Version field** incremented only on state changes, not heartbeats
- **SQS messages** deleted only after successful job completion
- **Idempotency** required at every step (SQS is at-least-once)
- **Secrets** go in AWS Secrets Manager; non-secret config in SSM Parameter Store
- **No always-on infrastructure** — everything is on-demand

## Code Organization

```
nemuri/
├── cmd/
│   ├── agent-engine/        # Main agent binary (runs on ECS)
│   ├── lambda-ingress/      # Discord → SQS (API Gateway Lambda)
│   └── lambda-runner/       # SQS → ECS RunTask (Lambda)
├── internal/
│   ├── agent/               # Agent loop, Reviewer, Rewriter, prompts
│   ├── llm/                 # LLM Adapter interface + Claude implementation
│   ├── state/               # DynamoDB state management, transitions
│   ├── converter/           # Markdown → HTML → PDF conversion
│   ├── discord/             # Discord API client
│   ├── github/              # GitHub API client (repo, content, PR)
│   ├── secrets/             # AWS Secrets Manager wrapper
│   └── storage/             # S3 operations wrapper
├── terraform/
│   ├── envs/dev/
│   ├── envs/prod/
│   └── modules/
│       ├── network/
│       ├── ecr/
│       ├── ecs/
│       ├── sqs/
│       ├── lambda_ingress/
│       ├── lambda_runner/
│       ├── dynamodb/
│       ├── s3/
│       └── iam/
├── Dockerfile
├── go.mod
└── go.sum
```

## Development Guidelines

- Write idiomatic Go; follow standard project layout
- Keep the Agent Engine as a "thin orchestrator" — logic should be in well-separated packages
- LLM calls go through the adapter interface (support future model swapping)
- External service interactions are isolated in dedicated packages (`github/`, `discord/`, `storage/`)
- Prefer structured JSON output from LLM calls
- Infrastructure changes must go through Terraform (no manual AWS console changes)
- **Keep documentation in sync**: When code, architecture, or conventions change, update the relevant markdown files (`CLAUDE.md`, `README.md`, `SPEC.md`, `PLAN.md`, `TODO.md`, `KNOWLEDGE.md`) as part of the same change. Do not leave documentation out of date.
