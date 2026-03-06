variable "project" {
  description = "Project name"
  type        = string
}

variable "environment" {
  description = "Environment name"
  type        = string
}

variable "aws_region" {
  description = "AWS region"
  type        = string
}

variable "ecr_repository_url" {
  description = "ECR repository URL for the agent-engine image"
  type        = string
}

variable "task_cpu" {
  description = "CPU units for the ECS task (1 vCPU = 1024)"
  type        = string
  default     = "256"
}

variable "task_memory" {
  description = "Memory (MiB) for the ECS task"
  type        = string
  default     = "512"
}

variable "dynamodb_table_name" {
  description = "DynamoDB jobs table name"
  type        = string
}

variable "dynamodb_table_arn" {
  description = "DynamoDB jobs table ARN"
  type        = string
}

variable "sqs_queue_url" {
  description = "SQS job queue URL"
  type        = string
}

variable "sqs_queue_arn" {
  description = "SQS job queue ARN"
  type        = string
}
