output "interactions_url" {
  description = "Discord Interaction Endpoint URL"
  value       = module.lambda_ingress.interactions_url
}

output "api_endpoint" {
  description = "API Gateway endpoint URL"
  value       = module.lambda_ingress.api_endpoint
}
