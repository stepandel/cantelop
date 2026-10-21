import unittest

from support import role_file_text, task, tasks_text


ROLE = "fire_fuse_benchmark"


class FireFuseBenchmarkCatalogTest(unittest.TestCase):
    def test_control_plane_requests_are_authenticated(self) -> None:
        defaults = role_file_text(ROLE, "defaults/main.yml")
        tasks = tasks_text(ROLE)

        self.assertIn(
            "fire_fuse_benchmark_control_token_file: /etc/fire-fuse/control-clients/operator.token",
            defaults,
        )
        self.assertIn("Require the trusted control-plane token", tasks)
        self.assertIn("fire_fuse_benchmark_control_token is match", tasks)
        self.assertGreaterEqual(
            tasks.count(
                'Authorization: "Bearer {{ fire_fuse_benchmark_control_token }}"'
            ),
            10,
        )

    def test_create_requests_use_sandbox_ids(self) -> None:
        tasks = tasks_text(ROLE)

        self.assertEqual(tasks.count("Idempotency-Key:"), 0)
        self.assertIn("'id': fire_fuse_benchmark_run_id ~ '-single'", tasks)
        self.assertIn("'id': fire_fuse_benchmark_run_id ~ '-cohort-' ~ item", tasks)
        self.assertEqual(tasks.count("'app_id': fire_fuse_benchmark_app_id"), 4)
        self.assertEqual(tasks.count("'profile': fire_fuse_benchmark_profile"), 2)
        self.assertEqual(tasks.count("'vcpus': fire_fuse_benchmark_vcpus | int"), 1)
        self.assertEqual(tasks.count("'memory_mib': fire_fuse_benchmark_memory_mib | int"), 1)

    def test_reads_images_from_the_trusted_active_catalog(self) -> None:
        tasks = tasks_text(ROLE)

        self.assertIn(
            "fire_fuse_benchmark_active_config.image_catalog\n"
            "        == '/etc/fire-fuse/image-catalog.json'",
            tasks,
        )
        catalog = task(ROLE, "Require the trusted active fire-fuse image catalog")
        self.assertIn(
            "fire_fuse_benchmark_catalog_status.stat "
            "is trusted_file(owner='root', group='root', mode='0640')",
            catalog["ansible.builtin.assert"]["that"],
        )
        self.assertIn("fire_fuse_benchmark_active_catalog.images", tasks)
        self.assertIn("fire_fuse_benchmark_catalog_content.content", tasks)
        self.assertIn("selectattr('name', 'equalto', 'image-catalog')", tasks)
        self.assertNotIn("fire_fuse_benchmark_active_config.images", tasks)

    def test_benchmark_app_has_a_canonical_session_runtime(self) -> None:
        defaults = role_file_text(ROLE, "defaults/main.yml")
        tasks = tasks_text(ROLE)

        self.assertIn("'build_id': fire_fuse_benchmark_build_id", tasks)
        self.assertIn("'slug': fire_fuse_benchmark_app_slug", tasks)
        self.assertIn("fire_fuse_benchmark_compute_ingress_address: 10.77.0.1", defaults)
        self.assertIn("fire_fuse_benchmark_compute_ingress_address == '10.77.0.1'", tasks)
        self.assertIn("fire_fuse_benchmark_compute_ingress_port: 8084", defaults)
        self.assertIn("/v1/internal/users/{{", tasks)
        self.assertNotIn("/v1/internal/users/resolve", tasks)
        self.assertNotIn("/v1/internal/admin/users/", tasks)
        self.assertIn("fire_fuse_benchmark_runtime_port: 3000", defaults)
        self.assertIn("'internal_port': fire_fuse_benchmark_runtime_port | int", tasks)
        self.assertIn("fire_fuse_benchmark_runtime_port | int >= 1", tasks)
        self.assertIn("fire_fuse_benchmark_runtime_port | int <= 65535", tasks)
        self.assertIn("'profile': fire_fuse_benchmark_profile", tasks)
        self.assertIn("- name: Register the benchmark Workspace", tasks)
        self.assertIn("/workspaces/{{ fire_fuse_benchmark_workspace }}", tasks)
        self.assertNotIn("'workspace': {", tasks)


if __name__ == "__main__":
    unittest.main()
