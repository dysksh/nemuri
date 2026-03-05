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

output "ecr_repository_url" {
  description = "ECR repository URL"
  value       = module.ecr.repository_url
}

output "ecs_cluster_name" {
  description = "ECS cluster name"
  value       = module.ecs.cluster_name
}

output "ecs_task_definition_family" {
  description = "ECS task definition family"
  value       = module.ecs.task_definition_family
}

output "vpc_id" {
  description = "VPC ID"
  value       = module.network.vpc_id
}
