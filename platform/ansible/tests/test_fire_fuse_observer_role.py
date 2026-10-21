from pathlib import Path
import unittest


ANSIBLE = Path(__file__).parents[1]
ROLE = ANSIBLE / "roles" / "compute" / "fire_fuse_observer"


class FireFuseObserverRoleTest(unittest.TestCase):
    def test_role_requires_checksum_and_vault_token(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("vault_fire_fuse_observer_token", defaults)
        self.assertIn("vault_fire_fuse_observer_runtime_token", defaults)
        self.assertIn("fire_fuse_observer_binary_checksum", tasks)
        self.assertIn("fire_fuse_observer_token is match('^[0-9a-f]{64}$')", tasks)
        self.assertIn('mode: "0640"', tasks)
        self.assertIn("Create the restricted observer user", tasks)
        self.assertIn("Install the private observer runtime credential", tasks)
        self.assertIn("group: \"{{ fire_fuse_observer_group }}\"", tasks)
        self.assertIn("no_log: true", tasks)

    def test_nginx_owns_only_observer_and_scoped_mutation_endpoints(self) -> None:
        nginx = (ROLE / "templates" / "nginx.conf.j2").read_text(
            encoding="utf-8"
        )

        self.assertIn("server_name {{ fire_fuse_observer_hostname }};", nginx)
        self.assertIn("location = /snapshot", nginx)
        self.assertIn("proxy_pass http://127.0.0.1:8082/snapshot;", nginx)
        self.assertIn("location = /requests", nginx)
        self.assertIn("proxy_pass http://127.0.0.1:8082/requests;", nginx)
        self.assertNotIn("location = /messages", nginx)
        self.assertIn("location = /infrastructure", nginx)
        self.assertIn(
            "proxy_pass http://127.0.0.1:8082/infrastructure;", nginx
        )
        self.assertIn(
            'location ~ "^/apps/[A-Za-z0-9][A-Za-z0-9._-]{0,63}/builds$"',
            nginx,
        )
        self.assertIn(
            'location ~ "^/sandboxes/sbx-[0-9a-f]{32}/database-access$"', nginx
        )
        self.assertEqual(nginx.count("limit_except GET { deny all; }"), 2)
        self.assertIn(
            'location ~ "^/sessions/ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$"',
            nginx,
        )
        self.assertNotIn(
            "location ~ ^/sessions/ses_[A-Za-z0-9][A-Za-z0-9_-]{0,59}$",
            nginx,
        )
        self.assertIn('location ~ "^/sandboxes/sbx-[0-9a-f]{32}$"', nginx)
        self.assertIn(
            'location ~ "^/sandboxes/sbx-[0-9a-f]{32}/crash$"', nginx
        )
        self.assertEqual(nginx.count("limit_except DELETE { deny all; }"), 2)
        self.assertEqual(nginx.count("limit_except POST { deny all; }"), 1)
        self.assertIn("proxy_pass_request_body off;", nginx)
        self.assertIn("location /", nginx)
        self.assertIn("return 404;", nginx)

    def test_role_qualifies_authentication_and_tls(self) -> None:
        defaults = (ROLE / "defaults" / "main.yml").read_text(encoding="utf-8")
        tasks = (ROLE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn(
            "/etc/letsencrypt/live/{{ fire_fuse_observer_domain }}/fullchain.pem", defaults
        )
        self.assertIn(
            "/etc/letsencrypt/live/{{ fire_fuse_observer_domain }}/privkey.pem", defaults
        )
        self.assertNotIn("/etc/fire-fuse-gateway/tls", defaults)
        self.assertIn("http://127.0.0.1:8082/snapshot", tasks)
        self.assertIn("fire_fuse_observer_snapshot.json.sessions is iterable", tasks)
        self.assertNotIn("/messages", tasks)
        self.assertIn("reject an unauthenticated snapshot", tasks)
        self.assertIn("authenticated snapshot through public TLS", tasks)
        self.assertIn("fire_fuse_observer_tls_qualification.stdout == '401'", tasks)
        self.assertIn("fire_fuse_observer_authenticated_qualification.stdout == '200'", tasks)
        self.assertIn("/sandboxes/sbx-00000000000000000000000000000000/database-access", tasks)
        self.assertIn("fire_fuse_observer_database_access_qualification.stdout == '401'", tasks)
        self.assertNotIn("--insecure", tasks)


if __name__ == "__main__":
    unittest.main()
