import importlib.util
from contextlib import closing, contextmanager
import json
import sqlite3
from pathlib import Path
import tempfile
import unittest
import unittest.mock
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]


def load(name, path):
    specification = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    return module


ship = load("local_ship", ROOT / "scripts/ship.py")
host = load("ship_host", ROOT / "platform/ansible/scripts/ship-component.py")


@contextmanager
def open_database(path):
    with closing(sqlite3.connect(path)) as connection, connection:
        yield connection


class RecordingRunner:
    def __init__(self, evidence, fail=None, needs_update=True):
        self.evidence = evidence
        self.commands = []
        self.fail = fail
        self.needs_update = needs_update

    def run(self, step, command, **kwargs):
        self.commands.append((step, command, kwargs))
        if step == self.fail:
            raise RuntimeError("injected failure")
        if step in ("host-preflight", "compute-client-preflight"):
            ship.write_json(self.evidence / "host-status" / (step + ".json"), {"needs_update": self.needs_update})


class ControllerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.artifacts = self.root / "artifacts"
        self.artifacts.mkdir()
        (self.artifacts / "binary").write_bytes(b"candidate")
        ship.write_json(self.artifacts / "manifest.json", {"source_commit": "a" * 40, "state_schema": 26})
        self.inventory = self.root / "hosts.yml"
        self.inventory.touch()
        self.vault = self.root / "vault-password"
        self.vault.touch()
        self.settings = {"inventory": str(self.inventory), "vault_password_file": str(self.vault)}
        self.source = self.root / "source"

    def tearDown(self):
        self.temporary.cleanup()

    def test_runtime_restores_ingress_after_failed_activation(self):
        runner = RecordingRunner(self.root, "deploy-component")
        with self.assertRaises(RuntimeError):
            ship.deploy("fire-fuse", "runtime", self.artifacts, self.settings, self.root, runner, self.source)
        self.assertEqual([step for step, *_ in runner.commands], ["host-preflight", "enter-maintenance", "deploy-component", "restore-ingress"])
        self.assertTrue(all(kwargs["cwd"] == self.source / "platform/ansible" for *_, kwargs in runner.commands))
        variables = json.loads((self.root / "ansible-vars.json").read_text())
        self.assertEqual(variables["ship_hosts"], "fire_fuse_hosts")
        self.assertNotIn("cantelop_control_worker_binary_artifact", variables)

    def test_failed_preflight_never_enters_maintenance(self):
        runner = RecordingRunner(self.root, "host-preflight")
        with self.assertRaises(RuntimeError):
            ship.deploy("fire-fuse", "runtime", self.artifacts, self.settings, self.root, runner, self.source)
        self.assertEqual(len(runner.commands), 1)

    def test_preflight_option_never_stops_or_updates_services(self):
        runner = RecordingRunner(self.root)
        ship.deploy("fire-fuse", "runtime", self.artifacts, self.settings, self.root, runner, self.source, preflight_only=True)
        self.assertEqual([step for step, *_ in runner.commands], ["host-preflight"])

    def test_already_installed_runtime_never_enters_maintenance(self):
        runner = RecordingRunner(self.root, needs_update=False)
        ship.deploy("fire-fuse", "runtime", self.artifacts, self.settings, self.root, runner, self.source)
        self.assertEqual([step for step, *_ in runner.commands], ["host-preflight"])

    def test_materializer_never_deploys_runtime_or_worker(self):
        runner = RecordingRunner(self.root)
        ship.deploy("builder", "materializer", self.artifacts, self.settings, self.root, runner, self.source)
        self.assertEqual([step for step, *_ in runner.commands], ["host-preflight", "deploy-component"])
        variables = json.loads((self.root / "ansible-vars.json").read_text())
        self.assertEqual(variables["ship_hosts"], "control_builder_hosts")

    def test_worker_uses_only_its_dedicated_role_and_records_revision(self):
        (self.artifacts / "migrate").write_bytes(b"runner")
        (self.artifacts / "migrations").mkdir()
        (self.artifacts / "migrations/SHA256SUMS").write_text("checksums")
        runner = RecordingRunner(self.root)
        ship.deploy("builder", "worker", self.artifacts, self.settings, self.root, runner, self.source)
        self.assertEqual([step for step, *_ in runner.commands], ["deploy-worker", "record-worker-revision"])
        self.assertEqual(runner.commands[0][1][-1], "playbooks/builder/control-worker.yml")
        variables = json.loads((self.root / "ansible-vars.json").read_text())
        self.assertEqual(variables["cantelop_control_worker_binary_checksum"], ship.digest(self.artifacts / "binary"))

    def test_juicefs_preflights_both_consumers_before_fencing(self):
        runner = RecordingRunner(self.root)
        ship.deploy("storage", "juicefs", self.artifacts, self.settings, self.root, runner, self.source)
        self.assertEqual([step for step, *_ in runner.commands], ["host-preflight", "compute-client-preflight", "enter-maintenance", "deploy-component", "deploy-compute-client", "restore-ingress"])
        command = runner.commands[4][1]
        self.assertEqual(json.loads(command[-2])["ship_component"], "compute-juicefs")

    def test_manifest_cache_rejects_tampered_artifacts_and_wrong_source(self):
        manifest = {"source_commit": "a" * 40, "component": "runtime", "builder_sha256": ship.digest(ROOT / "scripts/ship.py"),
                    "artifacts": {"binary": ship.digest(self.artifacts / "binary")}}
        ship.write_json(self.artifacts / "manifest.json", manifest)
        self.assertTrue(ship.valid_artifacts(self.artifacts, "a" * 40, "runtime"))
        self.assertFalse(ship.valid_artifacts(self.artifacts, "b" * 40, "runtime"))
        (self.artifacts / "binary").write_bytes(b"changed")
        self.assertFalse(ship.valid_artifacts(self.artifacts, "a" * 40, "runtime"))

    def test_valid_cached_artifact_skips_build_and_tests(self):
        cache = self.root / "cache"
        directory = cache / "artifacts/fire-fuse-runtime" / ("a" * 40)
        directory.mkdir(parents=True)
        (directory / "binary").write_bytes(b"checked")
        ship.write_json(directory / "manifest.json", {"source_commit": "a" * 40, "component": "runtime", "builder_sha256": ship.digest(ROOT / "scripts/ship.py"),
                        "artifacts": {"binary": ship.digest(directory / "binary")}})
        runner = RecordingRunner(self.root)
        self.assertEqual(ship.prepare("fire-fuse", "runtime", "a" * 40, cache, runner), directory)
        self.assertEqual(runner.commands, [])

    def test_prepare_builds_with_the_canonical_surface_script(self):
        cache = self.root / "cache"
        cache.mkdir()
        source = self.root / "snapshot"
        runner = RecordingRunner(self.root)

        def build(step, command, **kwargs):
            runner.commands.append((step, command, kwargs))
            if step == "component-build":
                (Path(command[-1]) / "fire-fuse-gateway-linux-amd64").write_bytes(b"canonical")

        with patch.object(ship, "source_tree", return_value=source), patch.object(runner, "run", side_effect=build):
            directory = ship.prepare("fire-fuse", "gateway", "a" * 40, cache, runner)
        self.assertEqual([step for step, *_ in runner.commands], ["component-tests", "component-build"])
        self.assertEqual(runner.commands[1][1][1], str(source / "scripts/build-compute-linux-amd64.sh"))
        self.assertEqual((directory / "binary").read_bytes(), b"canonical")
        self.assertTrue(ship.valid_artifacts(directory, "a" * 40, "gateway"))

    def test_configure_saves_only_explicit_existing_paths(self):
        config = self.root / "ship.json"
        with patch.dict(ship.os.environ, {"CANTELOP_SHIP_CACHE_DIR": str(self.root / "transient")}):
            ship.main(["configure", "--config", str(config), "--inventory", str(self.inventory), "--vault-password-file", str(self.vault)])
        self.assertEqual(json.loads(config.read_text()), {"inventory": str(self.inventory.resolve()), "vault_password_file": str(self.vault.resolve())})

    def test_configure_rejects_a_missing_inventory(self):
        config = self.root / "ship.json"
        with self.assertRaisesRegex(RuntimeError, "inventory does not exist"):
            ship.main(["configure", "--config", str(config), "--inventory", str(self.root / "missing.yml"), "--vault-password-file", str(self.vault)])
        self.assertFalse(config.exists())


class HostTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)

    def tearDown(self):
        self.temporary.cleanup()

    def test_failed_health_check_restores_previous_binary(self):
        destination = self.root / "installed"
        destination.write_bytes(b"previous")
        candidate = self.root / "candidate"
        candidate.write_bytes(b"new")
        calls = []
        with patch.object(host, "preflight"), patch.object(host, "RELEASES", self.root / "releases"), \
                patch.dict(host.TARGETS, {"materializer": (str(destination), ["worker.service"], None)}), \
                patch.object(host, "active", return_value=True), patch.object(host, "command", side_effect=lambda *args: calls.append(args)), \
                patch.object(host, "wait_ready", side_effect=[RuntimeError("failed candidate"), None]), \
                patch.object(host, "quiesce_materializer") as quiesce:
            with self.assertRaisesRegex(RuntimeError, "failed candidate"):
                host.apply("materializer", candidate, host.digest(candidate), "a" * 40, "test-run")
        quiesce.assert_called_once_with(120)
        self.assertEqual(destination.read_bytes(), b"previous")
        self.assertEqual(sum(call == ("systemctl", "start", "worker.service") for call in calls), 2)

    def test_same_binary_is_not_restarted(self):
        binary = self.root / "binary"
        binary.write_bytes(b"same")
        with patch.object(host, "preflight"), patch.dict(host.TARGETS, {"juicefs": (str(binary), [], None)}), patch.object(host, "command") as command:
            self.assertFalse(host.apply("juicefs", binary, host.digest(binary), "a" * 40, "run"))
        command.assert_not_called()

    def test_runtime_schema_mismatch_is_rejected_without_migration(self):
        database = self.root / "state.db"
        with open_database(database) as connection:
            connection.execute("PRAGMA user_version=25")
        with patch.object(host, "STATE", database), patch.object(host, "trusted"):
            with self.assertRaisesRegex(RuntimeError, "unchanged state schema"):
                host.database_status(schema=26)
        with open_database(database) as connection:
            self.assertEqual(connection.execute("PRAGMA user_version").fetchone()[0], 25)

    def test_runtime_binary_rollback_preserves_current_database(self):
        database = self.root / "state.db"
        with open_database(database) as connection:
            connection.execute("CREATE TABLE work (value TEXT)")
            connection.execute("INSERT INTO work VALUES ('before')")
        destination = self.root / "installed"
        destination.write_bytes(b"previous")
        candidate = self.root / "candidate"
        candidate.write_bytes(b"new")
        running = False
        starts = 0

        def execute(*args):
            nonlocal running, starts
            if args == ("systemctl", "start", host.RUNTIME):
                running = True
                starts += 1
                if starts == 1:
                    with open_database(database) as connection:
                        connection.execute("INSERT INTO work VALUES ('after')")
            elif args == ("systemctl", "stop", host.RUNTIME):
                running = False

        with patch.object(host, "preflight"), patch.object(host, "STATE", database), patch.object(host, "RELEASES", self.root / "releases"), \
                patch.dict(host.TARGETS, {"runtime": (str(destination), [host.RUNTIME], "http://localhost/readyz")}), \
                patch.object(host, "active", side_effect=lambda _: running), patch.object(host, "command", side_effect=execute), \
                patch.object(host, "wait_ready", side_effect=[RuntimeError("failed candidate"), None]):
            with self.assertRaisesRegex(RuntimeError, "failed candidate"):
                host.apply("runtime", candidate, host.digest(candidate), "a" * 40, "test-run", schema=26)
        self.assertEqual(destination.read_bytes(), b"previous")
        with open_database(database) as connection:
            self.assertEqual(connection.execute("SELECT count(*) FROM work").fetchone()[0], 2)
        with open_database(self.root / "releases/test-run/state.db") as connection:
            self.assertEqual(connection.execute("SELECT count(*) FROM work").fetchone()[0], 1)

    def test_storage_health_failure_keeps_compute_fenced(self):
        receipt = self.root / "boundary.json"
        host.write_json(receipt, {"services": {service: True for service in [host.RUNTIME, *host.DEPENDENTS, host.TUNNEL]}})
        with patch.object(host, "trusted"), patch.object(host, "database_status"), patch.object(host, "command") as command, \
                patch.object(host, "wait_ready", side_effect=RuntimeError("backend not ready")), patch.object(host, "provider_ingress") as ingress:
            with self.assertRaises(RuntimeError):
                host.resume(receipt, "minio")
        command.assert_not_called()
        ingress.assert_not_called()

    def test_drain_timeout_persists_restoration_receipt_without_stopping_runtime(self):
        receipt = self.root / "boundary.json"
        calls = []
        with patch.object(host, "active", return_value=True), patch.object(host, "database_status", return_value=1), \
                patch.object(host.time, "monotonic", side_effect=[0, 2]), patch.object(host, "command", side_effect=lambda *args: calls.append(args)), \
                patch.object(host, "provider_ingress", side_effect=lambda closed: calls.append(("provider-ingress", closed))):
            with self.assertRaisesRegex(RuntimeError, "did not drain"):
                host.fence(receipt, 1)
        self.assertTrue(receipt.exists())
        self.assertEqual(calls, [("systemctl", "stop", host.TUNNEL), ("provider-ingress", True)])

    def test_resume_does_not_reopen_tunnel_when_runtime_readiness_fails(self):
        receipt = self.root / "boundary.json"
        host.write_json(receipt, {"services": {service: True for service in [host.RUNTIME, *host.DEPENDENTS, host.TUNNEL]}})
        calls = []
        with patch.object(host, "trusted"), patch.object(host, "database_status"), patch.object(host, "command", side_effect=lambda *args: calls.append(args)), \
                patch.object(host, "wait_ready", side_effect=RuntimeError("not ready")), patch.object(host, "provider_ingress") as ingress:
            with self.assertRaises(RuntimeError):
                host.resume(receipt)
        self.assertTrue(receipt.exists())
        self.assertNotIn(("systemctl", "start", host.TUNNEL), calls)
        ingress.assert_not_called()

    def test_fence_closes_both_ingress_paths_before_stopping_runtime(self):
        receipt = self.root / "boundary.json"
        calls = []
        with patch.object(host, "active", return_value=True), patch.object(host, "database_status", return_value=0), \
                patch.object(host, "command", side_effect=lambda *args: calls.append(args)), \
                patch.object(host, "provider_ingress", side_effect=lambda closed: calls.append(("provider-ingress", closed))):
            host.fence(receipt, 1)
        self.assertEqual(calls, [("systemctl", "stop", host.TUNNEL), ("provider-ingress", True), ("systemctl", "stop", host.RUNTIME)])

    def test_resume_restores_runtime_then_dependents_then_ingress(self):
        receipt = self.root / "run.boundary.json"
        host.write_json(receipt, {"services": {service: True for service in [host.RUNTIME, *host.DEPENDENTS, host.TUNNEL]}})
        calls = []
        with patch.object(host, "trusted"), patch.object(host, "database_status"), patch.object(host, "wait_ready"), \
                patch.object(host, "command", side_effect=lambda *args: calls.append(args)), \
                patch.object(host, "provider_ingress", side_effect=lambda closed: calls.append(("provider-ingress", closed))):
            self.assertTrue(host.resume(receipt))
        self.assertEqual(calls, [("systemctl", "start", service) for service in [host.RUNTIME, *host.DEPENDENTS]]
                         + [("provider-ingress", False), ("systemctl", "start", host.TUNNEL)])
        self.assertFalse(receipt.exists())
        self.assertTrue((self.root / "run.boundary.completed.json").exists())

    def test_drain_ignores_terminated_and_failed_sandboxes(self):
        database = self.root / "state.db"
        with open_database(database) as connection:
            connection.execute("CREATE TABLE sandboxes (state TEXT)")
            connection.executemany("INSERT INTO sandboxes VALUES (?)", [("terminated",), ("failed",), ("running",)])
            for table in ("message_executions (terminal INTEGER)", "message_deliveries (work_state TEXT)",
                          "session_work_leases (lease_id TEXT)", "app_build_operations (status TEXT)"):
                connection.execute(f"CREATE TABLE {table}")
        with patch.object(host, "STATE", database), patch.object(host, "trusted"):
            self.assertEqual(host.database_status(drain=True), 1)

    def test_readiness_without_probe_requires_a_stable_service(self):
        with patch.object(host, "active", return_value=True), patch.object(host, "restarts", side_effect=["0", "0", "1"]), \
                patch.object(host.time, "sleep"):
            with self.assertRaisesRegex(RuntimeError, "restarted"):
                host.wait_ready(["dispatcher.service"])
        with patch.object(host, "active", return_value=True), patch.object(host, "restarts", return_value="0"), \
                patch.object(host.time, "sleep") as sleep:
            host.wait_ready(["dispatcher.service"])
        self.assertEqual(sleep.call_count, 2)

    def test_readiness_retries_a_probe_until_it_passes(self):
        check = unittest.mock.Mock(side_effect=[RuntimeError("not mounted"), RuntimeError("not mounted"), None])
        with patch.object(host, "active", return_value=True), patch.object(host, "restarts", return_value="0"), \
                patch.object(host.time, "sleep"):
            host.wait_ready(["juicefs.service"], check=check)
        self.assertEqual(check.call_count, 3)

    def test_compute_juicefs_activation_waits_for_its_mounts(self):
        destination = self.root / "juicefs"
        destination.write_bytes(b"previous")
        candidate = self.root / "candidate"
        candidate.write_bytes(b"new")
        with patch.object(host, "preflight"), patch.object(host, "RELEASES", self.root / "releases"), \
                patch.dict(host.TARGETS, {"compute-juicefs": (str(destination), ["juicefs.service"], None)}), \
                patch.object(host, "active", side_effect=lambda service: service != host.RUNTIME), patch.object(host, "command"), \
                patch.object(host, "wait_ready") as wait_ready:
            self.assertTrue(host.apply("compute-juicefs", candidate, host.digest(candidate), "a" * 40, "test-run"))
        self.assertIs(wait_ready.call_args.args[3], host.mounted)
        self.assertEqual(destination.read_bytes(), b"new")

    def test_rollback_does_not_wait_for_a_service_that_was_stopped(self):
        destination = self.root / "installed"
        destination.write_bytes(b"previous")
        candidate = self.root / "candidate"
        candidate.write_bytes(b"new")
        with patch.object(host, "preflight"), patch.object(host, "RELEASES", self.root / "releases"), \
                patch.dict(host.TARGETS, {"gateway": (str(destination), ["gateway.service"], "http://localhost/readyz")}), \
                patch.object(host, "active", return_value=False), patch.object(host, "command"), \
                patch.object(host, "write_json", side_effect=OSError("disk full")), patch.object(host, "wait_ready") as wait_ready:
            with self.assertRaisesRegex(OSError, "disk full"):
                host.apply("gateway", candidate, host.digest(candidate), "a" * 40, "test-run")
        wait_ready.assert_not_called()
        self.assertEqual(destination.read_bytes(), b"previous")

    def test_materializer_drain_timeout_reopens_its_socket(self):
        socket = self.root / "materializer.sock"
        socket.touch(mode=0o660)
        with patch.object(host, "MATERIALIZER_SOCKET", socket), patch.object(host.time, "sleep"), \
                patch.object(host, "command", return_value=f"u_str ESTAB 0 0 {socket} 1 * 2"), \
                patch.object(host.time, "monotonic", side_effect=[0, 2]):
            with self.assertRaisesRegex(RuntimeError, "did not drain"):
                host.quiesce_materializer(1)
        self.assertEqual(socket.stat().st_mode & 0o777, 0o660)

    def test_installed_reports_only_shipments_still_in_place(self):
        releases = self.root / "releases"
        current, replaced = self.root / "gateway", self.root / "observer"
        current.write_bytes(b"shipped")
        replaced.write_bytes(b"reprovisioned")
        for run, destination, checksum in (("20260101T000000000000Z-aaaaaaaaaaaa", current, host.digest(current)),
                                           ("20260102T000000000000Z-aaaaaaaaaaaa", replaced, "0" * 64)):
            (releases / run).mkdir(parents=True)
            host.write_json(releases / run / "component.json", {"destination": str(destination), "sha256": checksum, "source_commit": "a" * 40})
        with patch.object(host, "RELEASES", releases):
            self.assertEqual(host.installed(), {str(current): {"source_commit": "a" * 40, "sha256": host.digest(current)}})

    def test_prune_keeps_records_and_recent_rollback_material(self):
        releases = self.root / "releases"
        runs = [f"2026010{day}T000000000000Z-aaaaaaaaaaaa" for day in range(1, 7)]
        for run in runs:
            (releases / run).mkdir(parents=True)
            for name in ("state.db", "runtime.previous", "runtime.json"):
                (releases / run / name).touch()
        for checksum in ("1" * 64, "2" * 64, "3" * 64, "4" * 64, "5" * 64):
            (releases / "artifacts" / checksum).mkdir(parents=True)
        with patch.object(host, "RELEASES", releases):
            host.prune(runs[-1], "1" * 64)
        self.assertEqual([run for run in runs if (releases / run / "state.db").exists()], runs[2:])
        self.assertTrue(all((releases / run / "runtime.json").exists() for run in runs))
        self.assertIn("1" * 64, [path.name for path in (releases / "artifacts").iterdir()])
        self.assertEqual(len(list((releases / "artifacts").iterdir())), 4)


if __name__ == "__main__":
    unittest.main()
