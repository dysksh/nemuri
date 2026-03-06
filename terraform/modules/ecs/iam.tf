# --- ECS Execution Role (for pulling images, writing logs) ---

data "aws_iam_policy_document" "ecs_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ecs_execution" {
  name               = "${var.project}-${var.environment}-ecs-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume_role.json
}

resource "aws_iam_role_policy_attachment" "ecs_execution" {
  role       = aws_iam_role.ecs_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# --- ECS Task Role (for application-level permissions) ---

resource "aws_iam_role" "ecs_task" {
  name               = "${var.project}-${var.environment}-ecs-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume_role.json
}

data "aws_iam_policy_document" "ecs_task_logs" {
  statement {
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["${aws_cloudwatch_log_group.agent_engine.arn}:*"]
  }
}

resource "aws_iam_role_policy" "ecs_task_logs" {
  name   = "cloudwatch-logs"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_logs.json
}

# --- DynamoDB Access ---

data "aws_iam_policy_document" "ecs_task_dynamodb" {
  statement {
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:Query",
    ]
    resources = [var.dynamodb_table_arn]
  }
}

resource "aws_iam_role_policy" "ecs_task_dynamodb" {
  name   = "dynamodb-access"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_dynamodb.json
}

# --- SQS Access (visibility extension + delete) ---

data "aws_iam_policy_document" "ecs_task_sqs" {
  statement {
    actions = [
      "sqs:ChangeMessageVisibility",
      "sqs:DeleteMessage",
    ]
    resources = [var.sqs_queue_arn]
  }
}

resource "aws_iam_role_policy" "ecs_task_sqs" {
  name   = "sqs-access"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_sqs.json
}

# --- Secrets Manager Access ---

data "aws_iam_policy_document" "ecs_task_secrets" {
  statement {
    actions = [
      "secretsmanager:GetSecretValue",
    ]
    resources = [
      var.anthropic_api_key_arn,
      var.discord_bot_token_arn,
    ]
  }
}

resource "aws_iam_role_policy" "ecs_task_secrets" {
  name   = "secrets-access"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_secrets.json
}
