#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

AWS_REGION="${AWS_REGION:-ap-northeast-1}"
PROJECT="${PROJECT:-nemuri}"
ENVIRONMENT="${ENVIRONMENT:-dev}"
AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
ECR_REPO="${AWS_ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com/${PROJECT}-${ENVIRONMENT}-agent-engine"

echo "Logging in to ECR..."
aws ecr get-login-password --region "$AWS_REGION" | \
  docker login --username AWS --password-stdin "${AWS_ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com"

echo "Building Docker image..."
docker build -t agent-engine "$PROJECT_ROOT"

echo "Tagging image..."
docker tag agent-engine:latest "${ECR_REPO}:latest"

echo "Pushing to ECR..."
docker push "${ECR_REPO}:latest"

echo "Done: ${ECR_REPO}:latest"
