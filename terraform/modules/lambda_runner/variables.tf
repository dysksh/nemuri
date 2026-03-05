variable "project" {
  description = "Project name"
  type        = string
}

variable "environment" {
  description = "Environment name"
  type        = string
}

variable "lambda_zip_path" {
  description = "Path to the Lambda deployment ZIP"
  type        = string
  default     = "dist/lambda-runner.zip"
}

variable "sqs_queue_arn" {
  description = "SQS queue ARN to trigger from"
  type        = string
}

variable "ecs_cluster_arn" {
  description = "ECS cluster ARN"
  type        = string
}

variable "ecs_task_definition_arn" {
  description = "ECS task definition ARN"
  type        = string
}

variable "ecs_subnet_ids" {
  description = "Subnet IDs for ECS tasks"
  type        = list(string)
}

variable "ecs_security_group_id" {
  description = "Security group ID for ECS tasks"
  type        = string
}

variable "ecs_execution_role_arn" {
  description = "ECS execution role ARN (for iam:PassRole)"
  type        = string
}

variable "ecs_task_role_arn" {
  description = "ECS task role ARN (for iam:PassRole)"
  type        = string
}
