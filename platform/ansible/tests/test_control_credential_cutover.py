from pathlib import Path
import unittest

from support import task


PLAYBOOK = Path(__file__).parents[1] / "playbooks" / "shared" / "control-credential-cutover.yml"
PREFLIGHT = (
    Path(__file__).parents[1]
    / "roles"
    / "shared"
    / "fire_fuse_control_cutover_preflight"
    / "tasks"
)


class ControlCredentialCutoverTest(unittest.TestCase):
    def test_cutover_stops_every_old_reader_before_replacing_credentials(self) -> None:
        playbook = PLAYBOOK.read_text(encoding="utf-8")
        preflight = playbook.index("role: fire_fuse_control_cutover_preflight")
        worker_preflight = playbook.index("tasks_from: worker")
        worker_stop = playbook.index("Stop the builder control worker")
        stop = playbook.index("Stop and disable credential-dependent services")
        runtime_stop = playbook.index("Stop and disable the runtime")
        service = playbook.index("role: fire_fuse_service")
        activate = playbook.index("role: fire_fuse_activate")
        gateway = playbook.index("role: fire_fuse_gateway")
        observer = playbook.index("role: fire_fuse_observer")
        worker = playbook.index("role: cantelop_control_worker")
        self.assertLess(preflight, worker_preflight)
        self.assertLess(worker_preflight, worker_stop)
        self.assertLess(worker_stop, stop)
        self.assertLess(stop, runtime_stop)
        self.assertLess(runtime_stop, service)
        self.assertLess(service, activate)
        self.assertLess(activate, gateway)
        self.assertLess(gateway, observer)
        self.assertLess(observer, worker)
        for unit in (
            "fire-fuse-observer.service",
            "fire-fuse-gateway.service",
            "fire-fuse-async-dispatcher.service",
            "cantelop-control-worker.service",
        ):
            self.assertIn(unit, playbook)
        self.assertIn("serial: 1", playbook)
        self.assertIn("enabled: false", playbook)
        self.assertEqual(playbook.count("any_errors_fatal: true"), 2)

    def test_control_worker_runs_only_on_the_builder_vm(self) -> None:
        playbook = PLAYBOOK.read_text(encoding="utf-8")
        host_cutover = playbook[
            playbook.index("- name: Atomically cut over") : playbook.index(
                "- name: Restart the builder control worker"
            )
        ]
        self.assertNotIn("cantelop_control_worker", host_cutover)
        self.assertNotIn("cantelop-control-worker.service", host_cutover)
        worker_play = playbook[playbook.index("- name: Restart the builder control worker") :]
        self.assertIn("hosts: control_builder_hosts", worker_play)
        host_tasks = (PREFLIGHT / "main.yml").read_text(encoding="utf-8")
        self.assertNotIn("cantelop_control_worker", host_tasks)

    def test_preflight_validates_all_secrets_and_artifacts_before_shutdown(self) -> None:
        tasks = (PREFLIGHT / "main.yml").read_text(encoding="utf-8")
        worker = (PREFLIGHT / "worker.yml").read_text(encoding="utf-8")
        artifacts = (PREFLIGHT / "artifacts.yml").read_text(encoding="utf-8")
        self.assertIn("| unique | length == 10", tasks)
        self.assertIn("include_tasks: artifacts.yml", tasks)
        self.assertIn("include_tasks: artifacts.yml", worker)
        self.assertIn("Require every cutover artifact before shutdown", artifacts)
        self.assertIn("cantelop_control_migrations_artifact", worker)
        self.assertIn("Verify every migration file against the controller manifest", worker)
        self.assertIn("cantelop_control_worker_cloudflare_account_id is match", worker)
        self.assertIn("cantelop_control_worker_user == 'cantelop-control'", worker)
        self.assertIn("cantelop_control_worker_unit_source.startswith('/')", worker)
        self.assertIn(
            "cantelop_control_worker_compute_ingress_url == 'http://10.77.0.1:8084'",
            worker,
        )
        self.assertIn(
            "fire_fuse_compute_ingress_token == cantelop_control_worker_compute_control_token",
            worker,
        )
        self.assertIn("fire_fuse_stage_orchestrator_checksum", tasks)
        self.assertIn("/usr/local/bin/fire-fuse", tasks)
        self.assertIn("observer TLS key", tasks)

    def test_preflight_follows_letsencrypt_tls_symlinks(self) -> None:
        inspection = task(
            "fire_fuse_control_cutover_preflight", "Inspect live activation prerequisites"
        )
        self.assertEqual(
            inspection["ansible.builtin.stat"]["follow"],
            "{{ item.follow | default(false) }}",
        )
        self.assertEqual(
            {item["name"] for item in inspection["loop"] if item.get("follow")},
            {"observer TLS certificate", "observer TLS key"},
        )
        prerequisites = task(
            "fire_fuse_control_cutover_preflight",
            "Require live activation prerequisites before shutdown",
        )
        self.assertIn(
            "(item.item.directory and item.stat is trusted_directory) "
            "or (not item.item.directory and item.stat is trusted_file)",
            prerequisites["ansible.builtin.assert"]["that"],
        )


if __name__ == "__main__":
    unittest.main()
