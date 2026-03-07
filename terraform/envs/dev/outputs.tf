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

output "dynamodb_table_name" {
  description = "DynamoDB jobs table name"
  value       = module.dynamodb.table_name
}

output "anthropic_api_key_secret_name" {
  description = "Secrets Manager name for Anthropic API key"
  value       = module.secrets.anthropic_api_key_name
}

output "discord_bot_token_secret_name" {
  description = "Secrets Manager name for Discord bot token"
  value       = module.secrets.discord_bot_token_name
}

output "vpc_id" {
  description = "VPC ID"
  value       = module.network.vpc_id
}

output "s3_bucket_name" {
  description = "S3 storage bucket name"
  value       = module.s3.bucket_name
}

output "github_pat_secret_name" {
  description = "Secrets Manager name for GitHub PAT"
  value       = module.secrets.github_pat_name
}
