resource "aws_cloudwatch_log_group" "lambda" {
  name              = "/aws/lambda/${var.project}-${var.environment}-ingress"
  retention_in_days = 14
}

resource "aws_lambda_function" "ingress" {
  function_name = "${var.project}-${var.environment}-ingress"
  role          = aws_iam_role.lambda.arn
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = ["x86_64"]
  timeout       = 10
  memory_size   = 128

  filename         = var.lambda_zip_path
  source_code_hash = filebase64sha256(var.lambda_zip_path)

  environment {
    variables = {
      DISCORD_PUBLIC_KEY  = var.discord_public_key
      SQS_QUEUE_URL       = var.sqs_queue_url
      DYNAMODB_TABLE_NAME = var.dynamodb_table_name
    }
  }

  depends_on = [aws_cloudwatch_log_group.lambda]
}

# --- API Gateway (HTTP API v2) ---

resource "aws_apigatewayv2_api" "interactions" {
  name          = "${var.project}-${var.environment}-interactions"
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_integration" "lambda" {
  api_id                 = aws_apigatewayv2_api.interactions.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.ingress.invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "post_interactions" {
  api_id    = aws_apigatewayv2_api.interactions.id
  route_key = "POST /interactions"
  target    = "integrations/${aws_apigatewayv2_integration.lambda.id}"
}

resource "aws_apigatewayv2_stage" "default" {
  api_id      = aws_apigatewayv2_api.interactions.id
  name        = "$default"
  auto_deploy = true
}

resource "aws_lambda_permission" "apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.ingress.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.interactions.execution_arn}/*/*"
}
