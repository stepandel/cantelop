variable "cloudflare_account_id" {
  description = "Cloudflare account containing the Workers and Zero Trust resources."
  type        = string
  sensitive   = true

  validation {
    condition     = can(regex("^[0-9a-f]{32}$", var.cloudflare_account_id))
    error_message = "cloudflare_account_id must be 32 lowercase hexadecimal characters."
  }
}

variable "cloudflare_zone_id" {
  description = "Cloudflare zone ID for cantelop.dev."
  type        = string
  sensitive   = true

  validation {
    condition     = can(regex("^[0-9a-f]{32}$", var.cloudflare_zone_id))
    error_message = "cloudflare_zone_id must be 32 lowercase hexadecimal characters."
  }
}

variable "origin_hostname" {
  description = "Access-protected hostname routed through the edge Tunnel."
  type        = string
  default     = "edge-origin.cantelop.dev"

  validation {
    condition     = var.origin_hostname == "edge-origin.cantelop.dev"
    error_message = "the initial edge origin hostname is fixed to edge-origin.cantelop.dev."
  }
}

variable "dispatcher_hostname" {
  description = "Wildcard public hostname handled by the dispatcher Worker."
  type        = string
  default     = "*.cantelop.dev"

  validation {
    condition     = var.dispatcher_hostname == "*.cantelop.dev"
    error_message = "the initial dispatcher hostname is fixed to *.cantelop.dev."
  }
}
