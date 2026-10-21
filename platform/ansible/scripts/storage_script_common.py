"""Helpers shared by the storage backup and benchmark scripts in this directory.

The scripts are installed side by side as standalone programs under
hyphenated names, so they import this module from their own directory:

    sys.path.insert(0, str(Path(__file__).resolve().parent))
    import storage_script_common as common  # noqa: E402

Every role that installs one of those scripts must install this module beside
it (see the storage_backup_common role).  Helpers take the calling script's
error class so each script keeps raising its own exception type.
"""

from __future__ import annotations

import hashlib
import importlib.util
from pathlib import Path
import stat
from types import ModuleType
from typing import Any


SCRIPT_DIRECTORY = Path(__file__).resolve().parent


def load_sibling(name: str, filename: str) -> ModuleType:
    """Load another hyphenated script installed in this directory."""

    path = SCRIPT_DIRECTORY / filename
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def require_mapping(value: Any, name: str, *, error: type[Exception]) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise error(f"{name} must be an object")
    return value


def require_exact_keys(
    value: dict[str, Any],
    expected: set[str],
    name: str,
    *,
    error: type[Exception],
    detailed: bool = False,
) -> None:
    """Require exactly the expected keys, optionally naming the difference."""

    if set(value) == expected:
        return
    if not detailed:
        raise error(f"{name} has an invalid shape")
    missing = sorted(expected - set(value))
    unexpected = sorted(set(value) - expected)
    details = []
    if missing:
        details.append(f"missing {', '.join(missing)}")
    if unexpected:
        details.append(f"unexpected {', '.join(unexpected)}")
    raise error(f"{name} has an invalid shape ({'; '.join(details)})")


def sha256_file(path: Path, *, error: type[Exception], message: str) -> str:
    """Return a file's SHA-256, raising ``error`` with ``path: message``."""

    digest = hashlib.sha256()
    try:
        with path.open("rb") as source:
            while chunk := source.read(1024 * 1024):
                digest.update(chunk)
    except OSError as exception:
        raise error(f"{path}: {message}") from exception
    return digest.hexdigest()


def require_private_file(
    path: Path,
    expected_owner_uid: int | None,
    *,
    error: type[Exception],
) -> None:
    """Require a regular, nonsymlinked, mode-0600 file with the expected owner."""

    try:
        metadata = path.lstat()
    except OSError as exception:
        raise error(f"{path}: file is not readable") from exception
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise error(f"{path}: must be a regular non-symlink file")
    if expected_owner_uid is not None and metadata.st_uid != expected_owner_uid:
        raise error(f"{path}: has an unexpected owner")
    if stat.S_IMODE(metadata.st_mode) != 0o600:
        raise error(f"{path}: must have mode 0600")
