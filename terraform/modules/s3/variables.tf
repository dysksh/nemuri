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
