output "queue_url" {
  description = "SQS job queue URL"
  value       = aws_sqs_queue.jobs.url
}

output "queue_arn" {
  description = "SQS job queue ARN"
  value       = aws_sqs_queue.jobs.arn
}

output "queue_name" {
  description = "SQS job queue name"
  value       = aws_sqs_queue.jobs.name
}

output "dlq_url" {
  description = "SQS dead letter queue URL"
  value       = aws_sqs_queue.dlq.url
}

output "dlq_arn" {
  description = "SQS dead letter queue ARN"
  value       = aws_sqs_queue.dlq.arn
}
