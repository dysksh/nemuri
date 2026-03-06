variable "project" {
  description = "Project name"
  type        = string
}

variable "environment" {
  description = "Environment (dev, prod)"
  type        = string
}

variable "discord_public_key" {
  description = "Discord application public key for signature verification"
  type        = string
  sensitive   = true
}

variable "lambda_zip_path" {
  description = "Path to the Lambda deployment ZIP file"
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

variable "dynamodb_table_name" {
  description = "DynamoDB jobs table name"
  type        = string
}

variable "dynamodb_table_arn" {
  description = "DynamoDB jobs table ARN"
  type        = string
}
