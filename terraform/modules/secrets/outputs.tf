output "anthropic_api_key_arn" {
  description = "ARN of the Anthropic API key secret"
  value       = aws_secretsmanager_secret.anthropic_api_key.arn
}

output "anthropic_api_key_name" {
  description = "Name of the Anthropic API key secret"
  value       = aws_secretsmanager_secret.anthropic_api_key.name
}

output "discord_bot_token_arn" {
  description = "ARN of the Discord bot token secret"
  value       = aws_secretsmanager_secret.discord_bot_token.arn
}

output "discord_bot_token_name" {
  description = "Name of the Discord bot token secret"
  value       = aws_secretsmanager_secret.discord_bot_token.name
}

output "github_pat_arn" {
  description = "ARN of the GitHub PAT secret"
  value       = aws_secretsmanager_secret.github_pat.arn
}

output "github_pat_name" {
  description = "Name of the GitHub PAT secret"
  value       = aws_secretsmanager_secret.github_pat.name
}
