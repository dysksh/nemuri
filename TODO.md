# TODO.md — Task Tracker

## Phase 0: Prerequisites

- [ ] Configure AWS CLI credentials
- [ ] Initialize Terraform (backend, provider config)
- [ ] Create Discord application in Developer Portal
- [ ] Add bot to Discord application
- [ ] Obtain Discord public key
- [ ] Register `/agent` slash command via Discord API

## Phase 1: Discord → Lambda (Ingress)

- [ ] Write Terraform module: `network` (VPC, subnets, NAT, SG)
- [ ] Write Terraform module: `lambda_ingress` (API Gateway + Lambda)
- [ ] Implement Lambda handler: signature verification
- [ ] Implement Lambda handler: PING → PONG
- [ ] Implement Lambda handler: slash command → deferred ACK (type=5)
- [ ] Set Interaction Endpoint URL in Discord Developer Portal
- [ ] Test: `/agent hello` → "Bot is thinking..." in Discord

## Phase 2: SQS Integration

- [ ] Write Terraform module: `sqs` (job queue + DLQ)
- [ ] Update Ingress Lambda: generate job_id (UUID)
- [ ] Update Ingress Lambda: send SQS message
- [ ] Test: slash command → message visible in SQS console

## Phase 3: ECS RunTask

- [ ] Write Terraform module: `ecr`
- [ ] Write Terraform module: `ecs` (cluster, task definition, IAM roles)
- [ ] Write Terraform module: `lambda_runner` (SQS trigger → RunTask)
- [ ] Create minimal Dockerfile (distroless + Go hello-world binary)
- [ ] Build and push image to ECR
- [ ] Test: slash command → ECS task runs → "hello" in CloudWatch

## Phase 4: DynamoDB State Management

- [ ] Write Terraform module: `dynamodb` (jobs table, GSI: thread_id)
- [ ] Update Ingress Lambda: create job record (state=INIT)
- [ ] Implement Agent Engine: DynamoDB lock acquisition (conditional write)
- [ ] Implement Agent Engine: state transition logic (allowed transitions map)
- [ ] Implement Agent Engine: heartbeat goroutine (3-min interval)
- [ ] Implement Agent Engine: SQS visibility timeout extension (3-min interval)
- [ ] Implement Agent Engine: update state to DONE + delete SQS message on completion
- [ ] Test: full job lifecycle visible in DynamoDB

## Phase 5: Claude API Integration

- [ ] Design LLM Adapter interface (for future model swapping)
- [ ] Implement Claude API client in Go
- [ ] Implement simple flow: prompt → Claude → text result
- [ ] Implement Discord follow-up message (POST via interaction token)
- [ ] Write Terraform module: `s3` (artifacts + outputs buckets)
- [ ] Test: `/agent <question>` → Claude answers → response in Discord

## Phase 6: GitHub & S3 Deliverables

- [ ] Create GitHub App with appropriate permissions
- [ ] Store GitHub App credentials in AWS Secrets Manager
- [ ] Implement Tool Executor: clone, checkout branch, commit, push
- [ ] Implement Tool Executor: create PR via GitHub API
- [ ] Implement Tool Executor: S3 upload + presigned URL generation
- [ ] Post PR link / presigned URL to Discord
- [ ] Test: `/agent build a Go REST API` → PR created → link in Discord

## Phase 7: Review Loop

- [ ] Define review output schema (JSON: scores + issues list)
- [ ] Implement Reviewer function (structured evaluation)
- [ ] Implement Rewriter function (partial fix of flagged issues only)
- [ ] Implement review loop controller (convergence detection, max_revision)
- [ ] Test: generated code → reviewed → issues fixed → PR created

## Phase 8: User Interaction

- [ ] Implement WAITING_USER_INPUT state and ECS graceful exit
- [ ] Implement Discord question posting to job thread
- [ ] Update Ingress Lambda: detect thread_id → look up job → enqueue resume
- [ ] Implement ECS resume from saved state
- [ ] Implement WAITING_APPROVAL state for PR merge flow
- [ ] Test: agent asks question → user answers → agent resumes and completes

## Future (Post-MVP)

- [ ] Multi-model review (security reviewer + style reviewer)
- [ ] User profile memory (DynamoDB: preferences, coding style, stack)
- [ ] Project snapshot memory (S3: repo structure summaries)
- [ ] On-demand FAISS embedding for project context
- [ ] Hybrid execution mode (short session for Q&A, one-shot for tasks)
- [ ] Agent persona configuration (YAML: tone, principles, forbidden actions)
- [ ] Team support (multi-tenant isolation, per-user permissions)
- [ ] Monitoring Lambda (detect stale heartbeats → mark jobs FAILED)
