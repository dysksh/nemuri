# --- ECS Cluster ---

resource "aws_ecs_cluster" "main" {
  name = "${var.project}-${var.environment}"

  setting {
    name  = "containerInsights"
    value = "disabled"
  }
}

# --- CloudWatch Log Group ---

resource "aws_cloudwatch_log_group" "agent_engine" {
  name              = "/ecs/${var.project}-${var.environment}/agent-engine"
  retention_in_days = 14
}

# --- ECS Task Definition ---

resource "aws_ecs_task_definition" "agent_engine" {
  family                   = "${var.project}-${var.environment}-agent-engine"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.task_cpu
  memory                   = var.task_memory
  execution_role_arn       = aws_iam_role.ecs_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([
    {
      name      = "agent-engine"
      image     = "${var.ecr_repository_url}:latest"
      essential = true

      environment = [
        { name = "DYNAMODB_TABLE_NAME", value = var.dynamodb_table_name },
        { name = "AWS_REGION", value = var.aws_region },
        { name = "ANTHROPIC_API_KEY_SECRET_NAME", value = var.anthropic_api_key_name },
        { name = "DISCORD_BOT_TOKEN_SECRET_NAME", value = var.discord_bot_token_name },
        { name = "GITHUB_PAT_SECRET_NAME", value = var.github_pat_name },
        { name = "S3_BUCKET_NAME", value = var.s3_bucket_name },
        { name = "DEFAULT_GITHUB_OWNER", value = var.default_github_owner },
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.agent_engine.name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "ecs"
        }
      }
    }
  ])
}
