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
  description = "Cloudflare zone ID containing the configured App domain."
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
  default     = null

  validation {
    condition     = var.origin_hostname == null || can(regex("^[a-z0-9.-]+$", var.origin_hostname))
    error_message = "origin_hostname must be a lowercase DNS hostname; it must match edge-origin.<app_domain>."
  }
}

variable "dispatcher_hostname" {
  description = "Wildcard public hostname handled by the dispatcher Worker."
  type        = string
  default     = null

  validation {
    condition     = var.dispatcher_hostname == null || can(regex("^\\*\\.[a-z0-9.-]+$", var.dispatcher_hostname))
    error_message = "dispatcher_hostname must be a wildcard DNS hostname matching *.<app_domain>."
  }
}

variable "app_domain" {
  description = "Exact App DNS suffix; one App slug is prepended to this domain."
  type        = string
  default     = "cantelop.dev"

  validation {
    condition     = length(var.app_domain) <= 189 && can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$", var.app_domain))
    error_message = "app_domain must be a lowercase DNS domain without a scheme, port, wildcard, or trailing dot."
  }
}

variable "zone_name" {
  description = "Cloudflare DNS zone name; defaults to app_domain."
  type        = string
  default     = null

  validation {
    condition     = var.zone_name == null || can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$", var.zone_name))
    error_message = "zone_name must be a lowercase DNS zone name."
  }
}

variable "resource_prefix" {
  description = "Distinct names for an independent deployment; existing hosted names default to cantelop."
  type        = string
  default     = "cantelop"

  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]{0,22}[a-z0-9])?$", var.resource_prefix))
    error_message = "resource_prefix must be a lowercase DNS label of at most 24 characters."
  }
}

variable "environment" {
  description = "Deployment environment suffix, also suitable for Turso environment names."
  type        = string
  default     = "staging"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{0,15}$", var.environment))
    error_message = "environment must be 1-16 lowercase letters/digits, starting with a letter."
  }
}
