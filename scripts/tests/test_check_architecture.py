from __future__ import annotations

from pathlib import Path
import tempfile
import unittest

from support import load_script


CHECKER = load_script("check-architecture.py")


class ArchitectureCheckTest(unittest.TestCase):
    def check_source(self, relative: str, source: str):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / relative
            path.parent.mkdir(parents=True)
            path.write_text(source, encoding="utf-8")
            return CHECKER.check(root)

    def test_core_accepts_external_web_client(self) -> None:
        violations = self.check_source("console/core/workspace-databases/provider.ts", 'import { createClient } from "@libsql/client/web";')
        self.assertEqual(violations, [])

    def test_gateway_accepts_runtime_contracts(self) -> None:
        violations = self.check_source(
            "fire-fuse/gateway/handler.go",
            'package gateway\nimport "github.com/stepandel/cantelop/fire-fuse/runtime/model"\n',
        )
        self.assertEqual(violations, [])

    def test_gateway_rejects_runtime_implementation(self) -> None:
        violations = self.check_source(
            "fire-fuse/gateway/handler.go",
            'package gateway\nimport "github.com/stepandel/cantelop/fire-fuse/runtime/microvm"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("gateway must not import", str(violations[0]))

    def test_dispatcher_rejects_gateway_implementation(self) -> None:
        violations = self.check_source(
            "fire-fuse/dispatcher/dispatcher.go",
            'package dispatcher\nimport "github.com/stepandel/cantelop/fire-fuse/gateway"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("dispatcher must not import", str(violations[0]))

    def test_request_capability_rejects_gateway_implementation(self) -> None:
        violations = self.check_source(
            "fire-fuse/sessionbroker/broker.go",
            'package sessionbroker\nimport "github.com/stepandel/cantelop/fire-fuse/gateway"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("capabilities must not import gateway", str(violations[0]))

    def test_message_delivery_rejects_gateway_implementation(self) -> None:
        violations = self.check_source(
            "fire-fuse/messagedelivery/delivery.go",
            'package messagedelivery\nimport "github.com/stepandel/cantelop/fire-fuse/gateway"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("capabilities must not import gateway", str(violations[0]))

    def test_console_core_rejects_web_alias(self) -> None:
        violations = self.check_source(
            "console/core/service.ts",
            'import { session } from "@/lib/user-session";\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("must not import Console Web", str(violations[0]))

    def test_host_rejects_runtime_implementation(self) -> None:
        violations = self.check_source(
            "fire-fuse/host/observer/client.go",
            'package observer\nimport "github.com/stepandel/cantelop/fire-fuse/runtime/microvm"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("host must use runtime model contracts", str(violations[0]))

    def test_runtime_provider_rejects_microvm_implementation(self) -> None:
        violations = self.check_source(
            "fire-fuse/runtime/runtimeprovider/firecracker/provider.go",
            'package firecracker\nimport "github.com/stepandel/cantelop/fire-fuse/runtime/microvm"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("runtime providers must use model contracts", str(violations[0]))

    def test_persistence_rejects_microvm_orchestration(self) -> None:
        violations = self.check_source(
            "fire-fuse/runtime/persistence/sqlite/store.go",
            'package sqlite\nimport "github.com/stepandel/cantelop/fire-fuse/runtime/microvm"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("persistence must not import orchestration", str(violations[0]))

    def test_microvm_rejects_direct_database_access(self) -> None:
        violations = self.check_source(
            "fire-fuse/runtime/microvm/store.go",
            'package microvm\nimport "database/sql"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("must use persistence repositories", str(violations[0]))

    def test_microvm_tests_may_inspect_database_invariants(self) -> None:
        violations = self.check_source(
            "fire-fuse/runtime/microvm/store_test.go",
            'package microvm\nimport "database/sql"\n',
        )
        self.assertEqual(violations, [])

    def test_builder_accepts_shared_image_contracts(self) -> None:
        violations = self.check_source(
            "builder/materializer/materializer.go",
            'package materializer\nimport "github.com/stepandel/cantelop/fire-fuse/guest-runtime/guestconfig"\n',
        )
        self.assertEqual(violations, [])

    def test_builder_rejects_compute_implementation(self) -> None:
        violations = self.check_source(
            "builder/materializer/materializer.go",
            'package materializer\nimport "github.com/stepandel/cantelop/fire-fuse/runtime/microvm"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("builder must use shared image contracts", str(violations[0]))

    def test_compute_rejects_builder_implementation(self) -> None:
        violations = self.check_source(
            "fire-fuse/runtime/microvm/build.go",
            'package microvm\nimport "github.com/stepandel/cantelop/builder/materializer"\n',
        )
        self.assertEqual(len(violations), 1)
        self.assertIn("compute must not import builder code", str(violations[0]))

if __name__ == "__main__":
    unittest.main()
