variable "environment" {
  description = "Environment name"
  type        = string
  default     = "dev"
}

variable "project" {
  description = "Project name"
  type        = string
  default     = "nemuri"
}

variable "aws_region" {
  description = "AWS region"
  type        = string
  default     = "ap-northeast-1"
}

variable "discord_public_key" {
  description = "Discord application public key"
  type        = string
}

variable "default_github_owner" {
  description = "Default GitHub owner (org or user) for code deliverables"
  type        = string
}

variable "account_id" {
  description = "AWS account ID (used for globally unique S3 bucket names)"
  type        = string
}
