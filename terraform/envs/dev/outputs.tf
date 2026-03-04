output "interactions_url" {
  description = "Discord Interaction Endpoint URL"
  value       = module.lambda_ingress.interactions_url
}

output "api_endpoint" {
  description = "API Gateway endpoint URL"
  value       = module.lambda_ingress.api_endpoint
}

output "sqs_queue_url" {
  description = "SQS job queue URL"
  value       = module.sqs.queue_url
}

output "sqs_dlq_url" {
  description = "SQS dead letter queue URL"
  value       = module.sqs.dlq_url
}
