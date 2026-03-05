output "lambda_function_arn" {
  description = "Runner Lambda function ARN"
  value       = aws_lambda_function.runner.arn
}

output "lambda_function_name" {
  description = "Runner Lambda function name"
  value       = aws_lambda_function.runner.function_name
}
