mock_provider "cloudflare" {}
mock_provider "random" {}

variables {
  cloudflare_account_id = "00000000000000000000000000000000"
  cloudflare_zone_id    = "00000000000000000000000000000000"
}

run "hosted_identity_is_preserved" {
  command = plan
  assert {
    condition     = cloudflare_workers_for_platforms_dispatch_namespace.user_workers.name == "cantelop-user-workers-staging" && cloudflare_workers_kv_namespace.app_routes.title == "cantelop-app-routes-staging"
    error_message = "Default resource names must preserve existing hosted resources."
  }
  assert {
    condition     = output.origin_url == "https://edge-origin.cantelop.dev" && cloudflare_dns_record.dispatcher.name == "*.cantelop.dev"
    error_message = "Default DNS identities must preserve existing hosted routing."
  }
}

run "alternate_domain_and_parent_zone" {
  command = plan
  variables {
    app_domain      = "agents.example.net"
    zone_name       = "example.net"
    resource_prefix = "cantelop-alpha"
    environment     = "alpha"
  }
  assert {
    condition     = output.origin_url == "https://edge-origin.agents.example.net" && cloudflare_dns_record.dispatcher.name == "*.agents.example.net"
    error_message = "DNS must use only the configured App domain."
  }
  assert {
    condition     = cloudflare_workers_for_platforms_dispatch_namespace.user_workers.name == "cantelop-alpha-user-workers-alpha" && contains(keys(cloudflare_workers_route.dispatcher_exclusion), "status.apps.agents.example.net/*") && length(cloudflare_workers_route.dispatcher_exclusion) == 5
    error_message = "Independent namespaces and protected infrastructure exclusions must agree."
  }
}

run "reject_wider_wildcard" {
  command = plan
  variables {
    app_domain          = "agents.example.net"
    dispatcher_hostname = "*.example.net"
  }
  expect_failures = [cloudflare_dns_record.dispatcher]
}

run "reject_foreign_origin" {
  command = plan
  variables {
    origin_hostname = "attacker.example.net"
  }
  expect_failures = [cloudflare_dns_record.dispatcher]
}

run "reject_foreign_zone" {
  command = plan
  variables {
    zone_name = "example.net"
  }
  expect_failures = [cloudflare_dns_record.dispatcher]
}
