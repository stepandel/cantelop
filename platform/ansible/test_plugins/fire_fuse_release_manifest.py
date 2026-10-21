"""The single definition of the staged fire-fuse release manifest shape.

fire_fuse_stage renders release.json (templates/release.json.j2) and checks
it with ``is fire_fuse_release_manifest``; fire_fuse_promote checks the same
file before trusting any path in it.  Change the shape here, in the
template, and in SCHEMA_VERSION together.
"""

from collections.abc import Mapping, Sequence


SCHEMA_VERSION = 3
MANIFEST_KEYS = frozenset({"schema_version", "artifacts", "image_catalog", "config"})
ARTIFACT_KEYS = frozenset({"name", "staged_path", "destination", "sha256", "mode"})
IMAGE_CATALOG_KEYS = frozenset(
    {"base_sha256", "staged_path", "destination", "mode", "sha256"}
)
CONFIG_KEYS = frozenset({"staged_path", "destination", "mode", "sha256"})


def _has_exact_keys(value, keys):
    return isinstance(value, Mapping) and set(value) == keys


def fire_fuse_release_manifest(manifest):
    """Return whether a decoded manifest has the exact schema-3 shape."""

    if not _has_exact_keys(manifest, MANIFEST_KEYS):
        return False
    version = manifest["schema_version"]
    # bool is an int subclass; Ansible may also wrap decoded ints in subclasses.
    if isinstance(version, bool) or not isinstance(version, int):
        return False
    if version != SCHEMA_VERSION:
        return False
    artifacts = manifest["artifacts"]
    if isinstance(artifacts, (str, bytes)) or not isinstance(artifacts, Sequence):
        return False
    return (
        all(_has_exact_keys(artifact, ARTIFACT_KEYS) for artifact in artifacts)
        and _has_exact_keys(manifest["image_catalog"], IMAGE_CATALOG_KEYS)
        and _has_exact_keys(manifest["config"], CONFIG_KEYS)
    )


class TestModule:
    def tests(self):
        return {"fire_fuse_release_manifest": fire_fuse_release_manifest}
