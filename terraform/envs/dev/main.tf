terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project     = var.project
      Environment = var.environment
      ManagedBy   = "terraform"
    }
  }
}

module "sqs" {
  source = "../../modules/sqs"

  project     = var.project
  environment = var.environment
}

module "lambda_ingress" {
  source = "../../modules/lambda_ingress"

  project            = var.project
  environment        = var.environment
  discord_public_key = var.discord_public_key
  lambda_zip_path    = "${path.module}/../../../dist/lambda-ingress.zip"
  sqs_queue_url      = module.sqs.queue_url
  sqs_queue_arn      = module.sqs.queue_arn
}
