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

# --- Secrets Manager ---

module "secrets" {
  source = "../../modules/secrets"

  project     = var.project
  environment = var.environment
}

# --- DynamoDB ---

module "dynamodb" {
  source = "../../modules/dynamodb"

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

  project                    = var.project
  environment                = var.environment
  discord_public_key         = var.discord_public_key
  lambda_zip_path            = "${path.module}/../../../dist/lambda-ingress.zip"
  sqs_queue_url              = module.sqs.queue_url
  sqs_queue_arn              = module.sqs.queue_arn
  dynamodb_table_name        = module.dynamodb.table_name
  dynamodb_table_arn         = module.dynamodb.table_arn
  dynamodb_gsi_thread_id_arn = module.dynamodb.table_gsi_thread_id_arn
}

# --- S3 ---

module "s3" {
  source = "../../modules/s3"

  project     = var.project
  environment = var.environment
  account_id  = var.account_id
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

  dynamodb_table_name = module.dynamodb.table_name
  dynamodb_table_arn  = module.dynamodb.table_arn

  anthropic_api_key_arn  = module.secrets.anthropic_api_key_arn
  anthropic_api_key_name = module.secrets.anthropic_api_key_name
  discord_bot_token_arn  = module.secrets.discord_bot_token_arn
  discord_bot_token_name = module.secrets.discord_bot_token_name
  github_pat_arn         = module.secrets.github_pat_arn
  github_pat_name        = module.secrets.github_pat_name

  s3_bucket_arn  = module.s3.bucket_arn
  s3_bucket_name = module.s3.bucket_name

  default_github_owner = var.default_github_owner
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
