from pathlib import Path
import unittest

from support import task


ROOT = Path(__file__).parents[1]
ROLES = ROOT / "roles"


class FireFuseUpgradeRollbackRolesTest(unittest.TestCase):
    def test_prepare_upgrade_captures_fixed_recovery_boundary(self) -> None:
        defaults = (
            ROLES / "compute" / "fire_fuse_prepare_upgrade" / "defaults" / "main.yml"
        ).read_text(encoding="utf-8")
        tasks = (
            ROLES / "compute" / "fire_fuse_prepare_upgrade" / "tasks" / "main.yml"
        ).read_text(encoding="utf-8")

        self.assertIn("PREPARE FIRE-FUSE PRODUCTION UPGRADE", tasks)
        self.assertIn("/var/lib/fire-fuse/release-rollbacks", defaults)
        self.assertIn("source: /var/lib/fire-fuse/state/state.db", defaults)
        self.assertIn("source: /etc/fire-fuse/config-encryption.key", defaults)
        self.assertIn("source: /usr/local/bin/fire-fuse", defaults)
        self.assertIn("Refuse an existing upgrade snapshot ID", tasks)
        self.assertIn("Require exact rollback copies", tasks)
        sources = task(
            "fire_fuse_prepare_upgrade",
            "Require trusted current release and recovery material",
        )
        self.assertIn(
            "item.stat is trusted_file(owner='root', group='root', modes=item.item.modes)",
            sources["ansible.builtin.assert"]["that"],
        )
        self.assertIn('modes: ["0600", "0640"]', defaults)
        self.assertNotIn("state: absent", tasks)

    def test_rollback_is_approval_gated_and_inactive(self) -> None:
        defaults = (
            ROLES / "compute" / "fire_fuse_rollback" / "defaults" / "main.yml"
        ).read_text(encoding="utf-8")
        tasks = (
            ROLES / "compute" / "fire_fuse_rollback" / "tasks" / "main.yml"
        ).read_text(encoding="utf-8")

        self.assertIn("RESTORE FIRE-FUSE PRODUCTION ROLLBACK", tasks)
        self.assertIn("destination: /var/lib/fire-fuse/state/state.db", defaults)
        self.assertIn("destination: /etc/fire-fuse/config-encryption.key", defaults)
        self.assertIn("Validate rollback manifest allowlist", tasks)
        self.assertIn("fire_fuse_rollback_declared[0].mode in item.modes", tasks)
        self.assertIn('modes: ["0600", "0640"]', defaults)
        self.assertIn("- -check-state-only", tasks)
        self.assertIn("Validate restored lifecycle state", tasks)
        self.assertNotIn("- -preflight-only", tasks)
        self.assertNotIn("legacy", tasks)
        self.assertIn("Require explicit activation after rollback", tasks)
        self.assertNotIn("state: started", tasks)

    def test_upgrade_and_rollback_playbooks_are_separate_checkpoints(self) -> None:
        prepare = (ROOT / "playbooks" / "compute" / "prepare-runtime-upgrade.yml").read_text(
            encoding="utf-8"
        )
        rollback = (ROOT / "playbooks" / "compute" / "rollback-runtime.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("role: fire_fuse_prepare_upgrade", prepare)
        self.assertIn("role: fire_fuse_rollback", rollback)
        self.assertNotIn("fire_fuse_activate", prepare + rollback)

    def test_promotion_binds_an_explicit_offline_state_cutover(self) -> None:
        defaults = (
            ROLES / "compute" / "fire_fuse_promote" / "defaults" / "main.yml"
        ).read_text(encoding="utf-8")
        tasks = (
            ROLES / "compute" / "fire_fuse_promote" / "tasks" / "main.yml"
        ).read_text(encoding="utf-8")

        self.assertIn("CANTELOP_FIRE_FUSE_STATE_CUTOVER", defaults)
        self.assertIn("CANTELOP_FIRE_FUSE_STATE_CUTOVER_SHA256", defaults)
        self.assertIn("PROMOTE OFFLINE FIRE-FUSE STATE CUTOVER", tasks)
        self.assertIn(
            "fire_fuse_promote_state_cutover_sha256\n"
            "          == fire_fuse_promote_state.sha256",
            tasks,
        )
        self.assertIn(
            "== fire_fuse_promote_state_rollback_entries[0].sha256",
            tasks,
        )
