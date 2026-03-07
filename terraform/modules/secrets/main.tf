# --- Secrets Manager (boxes only, values injected manually) ---
#
# Values are NOT managed by Terraform to avoid storing secrets in tfstate.
# After `terraform apply`, inject values via:
#   aws secretsmanager put-secret-value --secret-id <name> --secret-string <value>
#
# recovery_window_in_days = 0 enables immediate deletion (no recovery).
# This avoids name conflicts when recreating secrets in dev.
# For production, consider setting this to 7 or 30.

resource "aws_secretsmanager_secret" "anthropic_api_key" {
  name                    = "${var.project}/${var.environment}/anthropic-api-key"
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret" "discord_bot_token" {
  name                    = "${var.project}/${var.environment}/discord-bot-token"
  recovery_window_in_days = 0
}

# GitHub Fine-grained Personal Access Token
resource "aws_secretsmanager_secret" "github_pat" {
  name                    = "${var.project}/${var.environment}/github-pat"
  recovery_window_in_days = 0
}
