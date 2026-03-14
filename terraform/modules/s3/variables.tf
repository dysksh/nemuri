variable "project" {
  description = "Project name"
  type        = string
}

variable "environment" {
  description = "Environment name"
  type        = string
}

variable "artifacts_expiration_days" {
  description = "Number of days before artifacts are automatically deleted"
  type        = number
  default     = 30
}

variable "outputs_expiration_days" {
  description = "Number of days before outputs are automatically deleted"
  type        = number
  default     = 180
}

variable "account_id" {
  description = "AWS account ID (used for globally unique bucket names)"
  type        = string
}
