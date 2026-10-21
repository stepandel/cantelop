"""Shared helpers for the repository script tests.

Scripts under ``scripts/`` have hyphenated file names, so tests load them by
path instead of importing them.
"""

from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
from types import ModuleType


SCRIPTS = Path(__file__).resolve().parents[1]
ROOT = SCRIPTS.parent


def load_module(path: Path, module_name: str | None = None) -> ModuleType:
    """Load a Python file by path and register it under its module name."""

    name = module_name or path.stem.replace("-", "_")
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


def load_script(file_name: str, module_name: str | None = None) -> ModuleType:
    """Load a script from the repository scripts/ directory as a module."""

    return load_module(SCRIPTS / file_name, module_name)
