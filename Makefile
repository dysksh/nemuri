.PHONY: setup setup-hooks up down dev check build-lambda build-and-push-ecr terraform-apply put-secret deploy bootstrap register-commands register-endpoint test lint

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

