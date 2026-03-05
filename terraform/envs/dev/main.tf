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

# --- Networking ---

module "network" {
  source = "../../modules/network"

  project     = var.project
  environment = var.environment
}

# --- SQS ---

module "sqs" {
  source = "../../modules/sqs"

  project     = var.project
  environment = var.environment
}

# --- Lambda Ingress ---

module "lambda_ingress" {
  source = "../../modules/lambda_ingress"

  project            = var.project
  environment        = var.environment
  discord_public_key = var.discord_public_key
  lambda_zip_path    = "${path.module}/../../../dist/lambda-ingress.zip"
  sqs_queue_url      = module.sqs.queue_url
  sqs_queue_arn      = module.sqs.queue_arn
}

# --- ECR ---

module "ecr" {
  source = "../../modules/ecr"

  project     = var.project
  environment = var.environment
}

# --- ECS ---

module "ecs" {
  source = "../../modules/ecs"

  project            = var.project
  environment        = var.environment
  aws_region         = var.aws_region
  ecr_repository_url = module.ecr.repository_url
}

# --- Lambda Runner ---

module "lambda_runner" {
  source = "../../modules/lambda_runner"

  project                 = var.project
  environment             = var.environment
  lambda_zip_path         = "${path.module}/../../../dist/lambda-runner.zip"
  sqs_queue_arn           = module.sqs.queue_arn
  ecs_cluster_arn         = module.ecs.cluster_arn
  ecs_task_definition_arn = module.ecs.task_definition_arn
  ecs_subnet_ids          = module.network.public_subnet_ids
  ecs_security_group_id   = module.network.ecs_security_group_id
  ecs_execution_role_arn  = module.ecs.execution_role_arn
  ecs_task_role_arn       = module.ecs.task_role_arn
}
