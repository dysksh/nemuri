output "api_endpoint" {
  description = "API Gateway endpoint URL"
  value       = aws_apigatewayv2_api.interactions.api_endpoint
}

output "interactions_url" {
  description = "Full URL for Discord Interaction Endpoint"
  value       = "${aws_apigatewayv2_api.interactions.api_endpoint}/interactions"
}

output "lambda_function_arn" {
  description = "Lambda function ARN"
  value       = aws_lambda_function.ingress.arn
}

output "lambda_function_name" {
  description = "Lambda function name"
  value       = aws_lambda_function.ingress.function_name
}
