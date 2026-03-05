data "aws_iam_policy_document" "lambda_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda" {
  name               = "${var.project}-${var.environment}-runner-lambda"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
}

# --- CloudWatch Logs ---

data "aws_iam_policy_document" "lambda_logs" {
  statement {
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["${aws_cloudwatch_log_group.lambda.arn}:*"]
  }
}

resource "aws_iam_role_policy" "lambda_logs" {
  name   = "cloudwatch-logs"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda_logs.json
}

# --- SQS Read ---

data "aws_iam_policy_document" "lambda_sqs" {
  statement {
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:GetQueueAttributes",
    ]
    resources = [var.sqs_queue_arn]
  }
}

resource "aws_iam_role_policy" "lambda_sqs" {
  name   = "sqs-read"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda_sqs.json
}

# --- ECS RunTask ---

data "aws_iam_policy_document" "lambda_ecs" {
  statement {
    actions = [
      "ecs:RunTask",
    ]
    resources = ["${var.ecs_task_definition_arn}:*"]
  }
  statement {
    actions = [
      "iam:PassRole",
    ]
    resources = [
      var.ecs_execution_role_arn,
      var.ecs_task_role_arn,
    ]
  }
}

resource "aws_iam_role_policy" "lambda_ecs" {
  name   = "ecs-run-task"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda_ecs.json
}
