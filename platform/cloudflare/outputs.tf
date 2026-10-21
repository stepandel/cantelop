data "cloudflare_zero_trust_tunnel_cloudflared_token" "edge" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.edge.id
}

output "user_workers_dispatch_namespace" {
  description = "Untrusted Workers for Platforms namespace containing customer Workers."
  value       = cloudflare_workers_for_platforms_dispatch_namespace.user_workers.namespace_name
}

output "app_routes_kv_namespace_id" {
  description = "Terraform-owned KV namespace used by the public App dispatcher."
  value       = cloudflare_workers_kv_namespace.app_routes.id
}

output "tunnel_id" {
  description = "Stable ID of the remotely managed edge Tunnel."
  value       = cloudflare_zero_trust_tunnel_cloudflared.edge.id
}

output "tunnel_token" {
  description = "Token installed only on the Hetzner cloudflared connector."
  value       = data.cloudflare_zero_trust_tunnel_cloudflared_token.edge.token
  sensitive   = true
}

output "origin_url" {
  description = "Exact broker fetch origin."
  value       = "https://${var.origin_hostname}"
}

output "origin_access_client_id" {
  description = "Access Service Auth client ID installed on the broker Worker."
  value       = cloudflare_zero_trust_access_service_token.origin_broker.client_id
  sensitive   = true
}

output "origin_access_client_secret" {
  description = "Access Service Auth secret installed on the broker Worker."
  value       = cloudflare_zero_trust_access_service_token.origin_broker.client_secret
  sensitive   = true
}

output "edge_ingress_token" {
  description = "Shared broker-to-gateway credential installed in both secret stores."
  value       = random_id.edge_ingress_token.hex
  sensitive   = true
}
