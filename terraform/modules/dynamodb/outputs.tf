output "table_name" {
  description = "DynamoDB jobs table name"
  value       = aws_dynamodb_table.jobs.name
}

output "table_arn" {
  description = "DynamoDB jobs table ARN"
  value       = aws_dynamodb_table.jobs.arn
}

output "table_gsi_thread_id_arn" {
  description = "DynamoDB jobs table thread_id GSI ARN"
  value       = "${aws_dynamodb_table.jobs.arn}/index/thread_id-index"
}
