.PHONY: setup setup-hooks up down dev claude check build-lambda build-and-push-ecr terraform-apply put-secret deploy bootstrap register-commands register-endpoint test lint \
	eval-build eval-test eval-snapshot eval-sync-down eval-sync-up eval-run eval-compare eval-recheck eval-bootstrap

setup: 
	make setup-hooks
	go mod download
	@echo "Setup complete."

setup-hooks:
	cp scripts/pre-commit .git/hooks/pre-commit
	cp scripts/pre-push .git/hooks/pre-push
	chmod +x .git/hooks/pre-commit .git/hooks/pre-push
	@echo "Git hooks installed."

up:
	USER_UID=$$(id -u) USER_GID=$$(id -g) DEV_HOME=$$([ "$$(id -u)" = "0" ] && echo /root || echo /home/dev) \
	docker compose up -d --build

down:
	docker compose down --remove-orphans || true
	docker network rm nemuri_default 2>/dev/null || true

dev:
	docker compose exec dev bash

claude:
	docker compose exec claude claude

check:
	infracost breakdown --path terraform/envs/dev
	terraform -chdir=terraform/envs/dev init -backend-config=backend.conf
	terraform -chdir=terraform/envs/dev plan

build-lambda:
	./scripts/build_lambda_ingress.sh
	./scripts/build_lambda_runner.sh

build-and-push-ecr:
	./scripts/build_and_push.sh

terraform-apply:
	terraform -chdir=terraform/envs/dev init -backend-config=backend.conf
	terraform -chdir=terraform/envs/dev apply -auto-approve

put-secret:
	. ./.env && \
	printf '%s' "$$ANTHROPIC_API_KEY" | \
		aws secretsmanager put-secret-value \
			--secret-id nemuri/dev/anthropic-api-key \
			--secret-string file:///dev/stdin && \
	printf '%s' "$$DISCORD_BOT_TOKEN" | \
		aws secretsmanager put-secret-value \
			--secret-id nemuri/dev/discord-bot-token \
			--secret-string file:///dev/stdin && \
	printf '%s' "$$GITHUB_PAT" | \
		aws secretsmanager put-secret-value \
			--secret-id nemuri/dev/github-pat \
			--secret-string file:///dev/stdin

deploy:
	make build-lambda
	make terraform-apply
	make build-and-push-ecr
	make put-secret
	make register-commands
	make register-endpoint

bootstrap:
	./scripts/bootstrap_tfstate.sh

register-commands:
	. ./.env && ./scripts/register_commands.sh

register-endpoint:
	. ./.env && ./scripts/register_endpoint.sh

test:
	go test --count=1 -cover ./internal/... && \
	DISCORD_PUBLIC_KEY=0000000000000000000000000000000000000000000000000000000000000000 \
	SQS_QUEUE_URL=https://sqs.ap-northeast-1.amazonaws.com/000000000000/dummy \
	DYNAMODB_TABLE_NAME=dummy \
	go test --count=1 -cover ./cmd/lambda-ingress/ && \
	ECS_CLUSTER_ARN=arn:aws:ecs:ap-northeast-1:000000000000:cluster/dummy \
	ECS_TASK_DEFINITION_ARN=arn:aws:ecs:ap-northeast-1:000000000000:task-definition/dummy:1 \
	ECS_SUBNET_IDS='["subnet-dummy"]' \
	ECS_SECURITY_GROUP_ID=sg-dummy \
	go test --count=1 -cover ./cmd/lambda-runner/

lint:
	@test -z "$$(gofmt -l .)" || { gofmt -l -d .; exit 1; }
	golangci-lint run ./...
	terraform fmt -check -recursive terraform/
	tflint --chdir=terraform/envs/dev

# ─── Eval (品質評価フレームワーク) ───

# eval CLI のビルド
eval-build:
	go build -o bin/eval ./eval/cmd/eval

# eval パッケージのテスト
eval-test:
	go test --count=1 -cover ./eval/...

EVAL_SNAPSHOT  ?= nemuri-v1
EVAL_TRIALS    ?= 5
# ?= is recursively expanded: the $(shell ...) call runs only when EVAL_BUCKET is actually referenced,
# so non-eval targets (e.g. make build) do not invoke AWS CLI.
EVAL_BUCKET    ?= nemuri-eval-fixtures-$(shell aws sts get-caller-identity --query Account --output text 2>/dev/null)

# S3 バケット作成（初回のみ）
eval-bootstrap:
	./scripts/eval_bootstrap.sh

# 現在のリポジトリからスナップショット作成
eval-snapshot:
	@echo "Creating snapshot: $(EVAL_SNAPSHOT)"
	./scripts/eval_snapshot.sh $(EVAL_SNAPSHOT)

# S3 からスナップショットをダウンロード
eval-sync-down:
	aws s3 sync s3://$(EVAL_BUCKET)/snapshots/ eval/fixtures/snapshots/ --delete
	@echo "Snapshots downloaded from S3."

# スナップショットを S3 にアップロード
eval-sync-up:
	aws s3 sync eval/fixtures/snapshots/ s3://$(EVAL_BUCKET)/snapshots/ --delete
	@echo "Snapshots uploaded to S3."

# 評価実行
eval-run:
	. ./.env && ANTHROPIC_API_KEY=$$ANTHROPIC_API_KEY \
	go run ./eval/cmd/eval run --trials $(EVAL_TRIALS) $(EVAL_ARGS)

# 2 つの結果を比較
eval-compare:
	@if [ -z "$(A)" ] || [ -z "$(B)" ]; then echo "Usage: make eval-compare A=<run-a.json> B=<run-b.json>"; exit 1; fi
	go run ./eval/cmd/eval compare $(A) $(B)

# 過去の結果を現在の期待値で再評価
eval-recheck:
	@if [ -z "$(RUN)" ]; then echo "Usage: make eval-recheck RUN=<run.json>"; exit 1; fi
	go run ./eval/cmd/eval recheck $(RUN)

