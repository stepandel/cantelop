resource "random_id" "edge_ingress_token" {
  byte_length = 32
}

resource "cloudflare_workers_for_platforms_dispatch_namespace" "user_workers" {
  account_id = var.cloudflare_account_id
  name       = "${var.resource_prefix}-user-workers-${var.environment}"
}

resource "cloudflare_workers_kv_namespace" "app_routes" {
  account_id = var.cloudflare_account_id
  title      = "${var.resource_prefix}-app-routes-${var.environment}"

  lifecycle {
    prevent_destroy = true
  }
}

resource "cloudflare_zero_trust_tunnel_cloudflared" "edge" {
  account_id = var.cloudflare_account_id
  name       = "${var.resource_prefix}-edge-${var.environment}"
  config_src = "cloudflare"
}

resource "cloudflare_zero_trust_tunnel_cloudflared_config" "edge" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.edge.id
  source     = "cloudflare"

  config = {
    ingress = [
      {
        hostname = local.origin_hostname
        service  = "http://127.0.0.1:8081"
      },
      {
        service = "http_status:404"
      }
    ]
  }
}

resource "cloudflare_dns_record" "edge_origin" {
  zone_id = var.cloudflare_zone_id
  name    = local.origin_hostname
  content = "${cloudflare_zero_trust_tunnel_cloudflared.edge.id}.cfargotunnel.com"
  type    = "CNAME"
  proxied = true
  ttl     = 1
  comment = "Cantelop Access-protected edge Tunnel origin"
}

resource "cloudflare_dns_record" "dispatcher" {
  zone_id = var.cloudflare_zone_id
  name    = local.dispatcher_hostname
  content = "100::"
  type    = "AAAA"
  proxied = true
  ttl     = 1
  comment = "Originless wildcard for the Cantelop dispatcher Worker"

  lifecycle {
    precondition {
      condition     = local.dispatcher_hostname == "*.${var.app_domain}"
      error_message = "dispatcher_hostname must match *.<app_domain>; wider wildcard routes are rejected."
    }
    precondition {
      condition     = local.origin_hostname == "edge-origin.${var.app_domain}"
      error_message = "origin_hostname must match the reserved edge-origin.<app_domain> host."
    }
    precondition {
      condition     = var.app_domain == local.zone_name || endswith(var.app_domain, ".${local.zone_name}")
      error_message = "app_domain must be inside zone_name."
    }
  }
}

locals {
  dispatcher_route_exclusions = toset([
    "console.${var.app_domain}/*",
    "control.apps.${var.app_domain}/*",
    "edge-origin.${var.app_domain}/*",
    "status.apps.${var.app_domain}/*",
    "www.${var.app_domain}/*",
  ])
}

# A route without a script negates the less-specific dispatcher wildcard.
# Keep these alongside the DNS migration so infrastructure hosts can never be
# captured by the public dispatcher route. The .apps hosts
# are listed explicitly because a wildcard exclusion would collide with the
# dispatcher's pre-migration route during the staged rollout.
resource "cloudflare_workers_route" "dispatcher_exclusion" {
  for_each = local.dispatcher_route_exclusions

  zone_id = var.cloudflare_zone_id
  pattern = each.value
}

resource "cloudflare_zero_trust_access_service_token" "origin_broker" {
  account_id = var.cloudflare_account_id
  name       = "${var.resource_prefix}-origin-broker-${var.environment}"
  duration   = "8760h"
}

resource "cloudflare_zero_trust_access_application" "edge_origin" {
  account_id                = var.cloudflare_account_id
  name                      = "${title(var.resource_prefix)} edge origin ${var.environment}"
  domain                    = local.origin_hostname
  type                      = "self_hosted"
  app_launcher_visible      = false
  service_auth_401_redirect = true
  session_duration          = "24h"

  policies = [
    {
      name       = "Allow Cantelop origin broker"
      precedence = 1
      decision   = "non_identity"
      include = [
        {
          service_token = {
            token_id = cloudflare_zero_trust_access_service_token.origin_broker.id
          }
        }
      ]
    }
  ]
}
locals {
  origin_hostname     = coalesce(var.origin_hostname, "edge-origin.${var.app_domain}")
  dispatcher_hostname = coalesce(var.dispatcher_hostname, "*.${var.app_domain}")
  zone_name           = coalesce(var.zone_name, var.app_domain)
}
