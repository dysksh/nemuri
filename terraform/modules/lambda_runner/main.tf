resource "aws_cloudwatch_log_group" "lambda" {
  name              = "/aws/lambda/${var.project}-${var.environment}-runner"
  retention_in_days = 14
}

resource "aws_lambda_function" "runner" {
  function_name = "${var.project}-${var.environment}-runner"
  role          = aws_iam_role.lambda.arn
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = ["x86_64"]
  timeout       = 30
  memory_size   = 128

  filename         = var.lambda_zip_path
  source_code_hash = filebase64sha256(var.lambda_zip_path)

  environment {
    variables = {
      ECS_CLUSTER_ARN         = var.ecs_cluster_arn
      ECS_TASK_DEFINITION_ARN = var.ecs_task_definition_arn
      ECS_SUBNET_IDS          = jsonencode(var.ecs_subnet_ids)
      ECS_SECURITY_GROUP_ID   = var.ecs_security_group_id
    }
  }

  depends_on = [aws_cloudwatch_log_group.lambda]
}

# --- SQS Event Source Mapping ---

resource "aws_lambda_event_source_mapping" "sqs" {
  event_source_arn = var.sqs_queue_arn
  function_name    = aws_lambda_function.runner.arn
  batch_size       = 1
  enabled          = true
}
