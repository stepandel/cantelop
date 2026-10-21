import unittest

from support import module_args, task


class CaptureManifestRoleTest(unittest.TestCase):
    def test_manifest_is_built_as_one_native_mapping(self) -> None:
        build_task = task(
            "storage_backup_capture_manifest", "Build the strict backup manifest"
        )
        facts = module_args(build_task, "set_fact")
        self.assertEqual(list(facts), ["storage_backup_capture_manifest_document"])
        document = facts["storage_backup_capture_manifest_document"]
        self.assertTrue(document.startswith("{{"))
        self.assertTrue(document.rstrip().endswith("}}"))
        self.assertIn("'schema_version': 1", document)
        self.assertEqual(document.count("'size_bytes':"), 4)
        self.assertEqual(document.count("| int"), 4)


if __name__ == "__main__":
    unittest.main()
