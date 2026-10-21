import json
from pathlib import Path
import unittest

import jinja2

from support import ANSIBLE_ROOT, load_module, module_args, runs_before, task


MANIFEST = load_module(
    ANSIBLE_ROOT / "test_plugins" / "fire_fuse_release_manifest.py"
)
ROLES =Path(__file__).parents[1] / "roles"
INVENTORY_EXAMPLE = (
    Path(__file__).parents[1]
    / "inventory"
    / "staging"
    / "hosts.example.yml"
)
STAGE = ROLES / "compute" / "fire_fuse_stage"
PROMOTE = ROLES / "compute" / "fire_fuse_promote"
SNAPSHOT = ROLES / "compute" / "fire_fuse_snapshot"
HOST_BASE = ROLES / "compute" / "host_base"
SERVICE = ROLES / "compute" / "fire_fuse_service"


class FireFuseReleaseRolesTest(unittest.TestCase):
    def test_scoped_server_credentials_are_root_only(self) -> None:
        tasks = (SERVICE / "tasks" / "main.yml").read_text(encoding="utf-8")
        directory = module_args(
            task(
                "fire_fuse_service",
                "Create the root-only scoped control credential directory",
            ),
            "file",
        )
        credentials = module_args(
            task("fire_fuse_service", "Install root-only scoped control-plane credentials"),
            "copy",
        )

        self.assertEqual(
            (directory["state"], directory["owner"], directory["group"], directory["mode"]),
            ("directory", "root", "root", "0700"),
        )
        self.assertEqual(
            (credentials["owner"], credentials["group"], credentials["mode"]),
            ("root", "root", "0600"),
        )
        self.assertIn("| unique | length == 5", tasks)

    def test_host_base_keeps_materializer_dependencies_off_compute(self) -> None:
        defaults = (HOST_BASE / "defaults" / "main.yml").read_text(
            encoding="utf-8"
        )

        self.assertNotIn("  - docker.io", defaults)
        self.assertIn("  - e2fsprogs", defaults)
        self.assertNotIn("  - path: /var/lib/fire-fuse/build-uploads", defaults)
        self.assertNotIn("  - path: /var/lib/fire-fuse/build-work", defaults)
        self.assertNotIn("/etc/fire-fuse/apps", defaults)

    def test_host_base_preserves_service_owned_directory_groups(self) -> None:
        directories = module_args(task("host_base", "Create host directories"), "file")

        self.assertEqual(directories["group"], "{{ item.group | default(omit) }}")
        self.assertEqual(directories["owner"], "root")

    def test_snapshot_role_requires_assignment_protocol_v2(self) -> None:
        defaults = (SNAPSHOT / "defaults" / "main.yml").read_text(
            encoding="utf-8"
        )
        tasks = (SNAPSHOT / "tasks" / "main.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("fire_fuse_snapshot_assignment_protocol: 2", defaults)
        self.assertIn(
            "== fire_fuse_snapshot_assignment_protocol | int", tasks
        )

    def test_manifest_has_a_strict_versioned_shape(self) -> None:
        template = (
            STAGE / "templates" / "release.json.j2"
        ).read_text(encoding="utf-8")
        stage_tasks = (
            STAGE / "tasks" / "main.yml"
        ).read_text(encoding="utf-8")
        promote_tasks = (
            PROMOTE / "tasks" / "main.yml"
        ).read_text(encoding="utf-8")

        stage_check = task(
            "fire_fuse_stage", "Require internally consistent staged release metadata"
        )
        promote_check = task("fire_fuse_promote", "Require the exact manifest shape")
        self.assertIn(
            "fire_fuse_stage_rendered_manifest is fire_fuse_release_manifest",
            stage_check["ansible.builtin.assert"]["that"],
        )
        self.assertIn(
            "fire_fuse_promote_release is fire_fuse_release_manifest",
            promote_check["ansible.builtin.assert"]["that"],
        )
        self.assertNotIn("rendered_manifest.schema_version", stage_tasks)
        self.assertNotIn("['artifacts', 'config', 'image_catalog'", promote_tasks)

        environment = jinja2.Environment(undefined=jinja2.StrictUndefined)
        environment.filters["to_json"] = json.dumps
        rendered = json.loads(
            environment.from_string(template).render(
                fire_fuse_stage_directory="/var/lib/fire-fuse/releases/staged",
                fire_fuse_stage_artifacts=[
                    {
                        "name": "fire-fuse",
                        "staged_name": "fire-fuse",
                        "destination": "/usr/local/bin/fire-fuse",
                        "checksum": "a" * 64,
                        "mode": "0755",
                    }
                ],
                fire_fuse_stage_existing_catalog_sha256="b" * 64,
                fire_fuse_stage_image_catalog_destination=(
                    "/etc/fire-fuse/image-catalog.json"
                ),
                fire_fuse_stage_image_catalog_sha256="c" * 64,
                fire_fuse_stage_config_destination="/etc/fire-fuse/config.json",
                fire_fuse_stage_config_sha256="d" * 64,
            )
        )
        self.assertEqual(rendered["schema_version"], MANIFEST.SCHEMA_VERSION)
        self.assertTrue(MANIFEST.fire_fuse_release_manifest(rendered))
        artifact = rendered["artifacts"][0]
        for broken in (
            {**rendered, "schema_version": True},
            {**rendered, "schema_version": "3"},
            {**rendered, "extra": 1},
            {**rendered, "artifacts": "fire-fuse"},
            {**rendered, "artifacts": [{**artifact, "extra": 1}]},
            {**rendered, "config": {"mode": "0640"}},
            {key: value for key, value in rendered.items() if key != "config"},
        ):
            self.assertFalse(MANIFEST.fire_fuse_release_manifest(broken), broken)
        self.assertIn(
            "fire_fuse_stage_rendered_manifest.config.sha256",
            stage_tasks,
        )
        self.assertIn(
            "== fire_fuse_promote_release.config.sha256",
            promote_tasks,
        )
        self.assertIn(
            "fire_fuse_stage_config_sha256 | to_json",
            template,
        )
        self.assertIn(
            "fire_fuse_stage_config_document | hash('sha256')",
            stage_tasks,
        )
        self.assertIn(
            "fire_fuse_stage_image_catalog_document | hash('sha256')",
            stage_tasks,
        )
        self.assertIn(
            "fire_fuse_stage_rendered_manifest.image_catalog.sha256",
            stage_tasks,
        )
        self.assertIn(
            "fire_fuse_promote_release.image_catalog.sha256",
            promote_tasks,
        )
        self.assertIn(
            "fire_fuse_stage_rendered_manifest.image_catalog.base_sha256",
            stage_tasks,
        )
        self.assertIn(
            "fire_fuse_promote_release.image_catalog.base_sha256",
            promote_tasks,
        )

    def test_image_root_filesystems_are_staged_and_promoted(self) -> None:
        stage_defaults = (
            STAGE / "defaults" / "main.yml"
        ).read_text(encoding="utf-8")
        stage_tasks = (
            STAGE / "tasks" / "main.yml"
        ).read_text(encoding="utf-8")
        promote_defaults = (
            PROMOTE / "defaults" / "main.yml"
        ).read_text(encoding="utf-8")
        promote_tasks = (
            PROMOTE / "tasks" / "main.yml"
        ).read_text(encoding="utf-8")

        for defaults in (stage_defaults, promote_defaults):
            self.assertIn("fire_fuse_release_images: []", defaults)

        self.assertIn(
            "Add declared images to the promotion allowlist",
            promote_tasks,
        )
        self.assertIn("Validate promotion image declarations", promote_tasks)
        self.assertIn("'name': 'container image ' ~ item.name", promote_tasks)
        self.assertNotIn("fire_fuse_promote_allowlist | length == 8", promote_tasks)
        derived_path = (
            "'/var/lib/fire-fuse/images/sha256-'\n"
            "          ~ item.rootfs_checksum ~ '.ext4'"
        )
        self.assertIn(derived_path, stage_tasks)
        self.assertIn(derived_path, promote_tasks)
        self.assertIn(
            "or item.stat.checksum | default('') == item.item.sha256",
            promote_tasks,
        )

        inventory = INVENTORY_EXAMPLE.read_text(encoding="utf-8")
        image_declaration = inventory.split("fire_fuse_release_images:", 1)[1]
        self.assertNotIn("staged_name:", image_declaration)
        self.assertNotIn("destination:", image_declaration)

    def test_stage_accepts_versioned_immutable_snapshot_bundles(self) -> None:
        tasks = (STAGE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn(
            "is match('^/var/lib/fire-fuse/snapshots/[A-Za-z0-9._-]+$')",
            tasks,
        )
        self.assertNotIn(
            "== '/var/lib/fire-fuse/snapshots/default-4vcpu-512m-hotplug-4g'",
            tasks,
        )

    def test_promotion_preserves_existing_lifecycle_state(self) -> None:
        defaults = (
            PROMOTE / "defaults" / "main.yml"
        ).read_text(encoding="utf-8")
        tasks = (
            PROMOTE / "tasks" / "main.yml"
        ).read_text(encoding="utf-8")

        self.assertNotIn("fire_fuse_promote_state_schema_versions", defaults)
        self.assertNotIn("fire_fuse_promote_state_schema_versions", tasks)
        self.assertIn("Inspect lifecycle state without modifying it", tasks)
        self.assertIn("inspect-runtime-state.py", tasks)
        self.assertIn(
            "Require a safe lifecycle-state inspection result",
            tasks,
        )
        self.assertIn("Require an upgrade snapshot for a state-bearing promotion", tasks)
        self.assertIn("Require rollback coverage for the exact live lifecycle state", tasks)
        self.assertIn("Require an intact lifecycle-state rollback copy", tasks)
        self.assertIn("fire_fuse_promote_state.sha256", tasks)
        self.assertNotIn("fail_msg: promotion requires an absent lifecycle database", tasks)
        self.assertNotIn("schema_version", tasks.split("Inspect the staged release manifest", 1)[0])

    def test_profile_process_is_a_verified_controller_input(self) -> None:
        defaults = (
            STAGE / "defaults" / "main.yml"
        ).read_text(encoding="utf-8")
        tasks = (
            STAGE / "tasks" / "main.yml"
        ).read_text(encoding="utf-8")
        inventory = INVENTORY_EXAMPLE.read_text(encoding="utf-8")

        self.assertIn("fire_fuse_release_images: []", defaults)
        self.assertIn("fire_fuse_release_images:", inventory)
        self.assertIn("process_artifact:", inventory)
        self.assertIn("process_checksum:", inventory)

        self.assertIn(
            "Verify image process artifacts on the controller",
            tasks,
        )
        self.assertIn(
            "../../../hetzner/validate-container-process.jq",
            tasks,
        )
        self.assertIn("Require jq on the Ansible controller", tasks)
        self.assertGreaterEqual(tasks.count("check_mode: false"), 2)
        self.assertIn(
            "== item.item.process_checksum",
            tasks,
        )
        self.assertNotIn("fire_fuse_stage_profile_process_artifact:", defaults)
        self.assertNotIn("fire_fuse_stage_profile_process_checksum:", defaults)

    def test_container_catalog_is_derived_and_rendered(self) -> None:
        tasks = (STAGE / "tasks" / "main.yml").read_text(
            encoding="utf-8"
        )
        template = (STAGE / "templates" / "config.json.j2").read_text(
            encoding="utf-8"
        )
        catalog_template = (
            STAGE / "templates" / "image-catalog.json.j2"
        ).read_text(encoding="utf-8")

        self.assertIn("Build content-addressed image catalog entries", tasks)
        self.assertIn("files/build-image-catalog.py", tasks)
        self.assertIn("item.rootfs_checksum", tasks)
        self.assertIn("item.process_artifact", tasks)
        self.assertIn("item.0.image.rootfs_size_bytes", tasks)
        self.assertIn("Require unique content-addressed image identities", tasks)
        self.assertIn("fire_fuse_stage_rendered_image_catalog.images", tasks)
        self.assertIn('"image_catalog":', template)
        self.assertIn('"config_encryption_key_file":', template)
        self.assertNotIn('"worker_platform":', template)
        self.assertNotIn('"api_token_file":', template)
        self.assertNotIn('"app_environment_directory":', template)
        self.assertIn("Refuse configuration encryption key rotation", tasks)
        self.assertNotIn("Validate the Workers for Platforms credentials", tasks)
        self.assertNotIn("Install the configuration encryption key", tasks)
        self.assertNotIn('"images":', template)
        self.assertIn('"schema_version": 1', catalog_template)
        self.assertIn('"images": {{ fire_fuse_stage_images', catalog_template)
        self.assertIn("Write the proposed image catalog", tasks)

    def test_prestaging_does_not_reconcile_the_active_host(self) -> None:
        playbook = (ROLES.parent / "playbooks" / "compute" / "stage-runtime.yml").read_text(encoding="utf-8")
        stage_tasks = (STAGE / "tasks" / "main.yml").read_text(encoding="utf-8")
        service_tasks = (SERVICE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertNotIn("role: host_base", playbook)
        self.assertIn("Require a trusted existing configuration encryption key", stage_tasks)
        self.assertIn("Install the configuration encryption key during inactive service preparation", service_tasks)
        self.assertTrue(
            runs_before(
                "fire_fuse_service",
                "Refuse to replace an active fire-fuse service",
                "Install the configuration encryption key during inactive service preparation",
            )
        )

    def test_live_catalog_entries_survive_runtime_staging(self) -> None:
        tasks = (STAGE / "tasks" / "main.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("Inspect the current trusted image catalog", tasks)
        self.assertIn("Require a trusted current image catalog", tasks)
        self.assertIn(
            'fire_fuse_stage_images: "{{ fire_fuse_stage_existing_catalog.images }}"',
            tasks,
        )
        self.assertIn(
            "Reject conflicts with previously published image identities",
            tasks,
        )

    def test_promotion_serializes_with_catalog_writers(self) -> None:
        stage_tasks = (STAGE / "tasks" / "main.yml").read_text(
            encoding="utf-8"
        )
        promote_tasks = (PROMOTE / "tasks" / "main.yml").read_text(
            encoding="utf-8"
        )
        coordinator = (
            PROMOTE / "files" / "coordinate-image-catalog.py"
        ).read_text(encoding="utf-8")

        self.assertIn("fire_fuse_stage_existing_catalog_sha256", stage_tasks)
        self.assertIn("Acquire the image catalog deployment lease", promote_tasks)
        self.assertIn("Release the image catalog deployment lease", promote_tasks)
        self.assertIn("coordinate-image-catalog.py begin", promote_tasks)
        self.assertIn("coordinate-image-catalog.py end", promote_tasks)
        self.assertIn('str(lock_path) + ".deployment"', coordinator)
        self.assertIn("live image catalog changed after staging", coordinator)
        self.assertIn("fcntl.LOCK_EX", coordinator)

    def test_image_publisher_is_a_promoted_release_artifact(self) -> None:
        stage_defaults = (STAGE / "defaults" / "main.yml").read_text(
            encoding="utf-8"
        )
        promote_defaults = (PROMOTE / "defaults" / "main.yml").read_text(
            encoding="utf-8"
        )
        inventory = INVENTORY_EXAMPLE.read_text(encoding="utf-8")

        self.assertIn("fire_fuse_stage_image_publisher_artifact", stage_defaults)
        self.assertIn("destination: /usr/local/bin/fire-fuse-image", stage_defaults)
        self.assertIn("destination: /usr/local/bin/fire-fuse-image", promote_defaults)
        self.assertIn("fire_fuse_stage_image_publisher_artifact:", inventory)
        self.assertIn("/release/bin/fire-fuse-image", inventory)

    def test_admin_client_is_a_promoted_release_artifact(self) -> None:
        stage_defaults = (STAGE / "defaults" / "main.yml").read_text(
            encoding="utf-8"
        )
        promote_defaults = (PROMOTE / "defaults" / "main.yml").read_text(
            encoding="utf-8"
        )
        inventory = INVENTORY_EXAMPLE.read_text(encoding="utf-8")

        self.assertIn("fire_fuse_stage_admin_artifact", stage_defaults)
        self.assertIn("destination: /usr/local/bin/cantelop", stage_defaults)
        self.assertIn("destination: /usr/local/bin/cantelop", promote_defaults)
        self.assertIn("fire_fuse_stage_admin_artifact:", inventory)
        self.assertIn("/release/bin/cantelop", inventory)

    def test_host_pool_is_explicitly_configured(self) -> None:
        defaults = (STAGE / "defaults" / "main.yml").read_text(encoding="utf-8")
        template = (STAGE / "templates" / "config.json.j2").read_text(encoding="utf-8")
        tasks = (STAGE / "tasks" / "main.yml").read_text(encoding="utf-8")

        self.assertIn("fire_fuse_stage_host_pool:", defaults)
        self.assertIn("slots_per_profile: 50", defaults)
        self.assertIn('"host_pool": {{ fire_fuse_stage_host_pool', template)
        self.assertIn("fire_fuse_stage_host_pool.slots_per_profile", tasks)


if __name__ == "__main__":
    unittest.main()
