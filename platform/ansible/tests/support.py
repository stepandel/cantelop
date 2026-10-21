"""Shared helpers for the Ansible role and script tests.

Scripts under ``platform/ansible/scripts`` have hyphenated file names, so
tests load them by path instead of importing them.  Role tests should prefer
the parsed-YAML helpers (``task``/``tasks``) over raw text searches so they
assert the field that matters rather than any occurrence in the file.
"""

from __future__ import annotations

from collections.abc import Iterator
import importlib.util
import sys
from pathlib import Path
from types import ModuleType
from typing import Any

import yaml


ANSIBLE_ROOT = Path(__file__).resolve().parents[1]
ROLES = ANSIBLE_ROOT / "roles"
SCRIPTS = ANSIBLE_ROOT / "scripts"
PLAYBOOKS = ANSIBLE_ROOT / "playbooks"

_INCLUDE_ACTIONS = (
    "ansible.builtin.import_tasks",
    "ansible.builtin.include_tasks",
    "import_tasks",
    "include_tasks",
)
_BLOCK_SECTIONS = ("block", "rescue", "always")


def load_module(path: Path, module_name: str | None = None) -> ModuleType:
    """Load a Python file by path, such as a hyphenated script or role file."""

    name = module_name or path.stem.replace("-", "_")
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


def load_script(file_name: str, module_name: str | None = None) -> ModuleType:
    """Load a script from platform/ansible/scripts as a module."""

    return load_module(SCRIPTS / file_name, module_name)


def role_path(role: str) -> Path:
    """Return the directory of a role regardless of its role group."""

    matches = [path for path in ROLES.glob(f"*/{role}") if path.is_dir()]
    if len(matches) != 1:
        raise LookupError(f"expected exactly one role named {role}: {matches}")
    return matches[0]


def role_file_text(role: str, relative: str) -> str:
    return (role_path(role) / relative).read_text(encoding="utf-8")


def tasks_text(role: str) -> str:
    """Return every task file of a role, main.yml first, as one string."""

    directory = role_path(role) / "tasks"
    files = sorted(directory.glob("*.yml"), key=lambda path: (path.name != "main.yml", path.name))
    return "\n".join(path.read_text(encoding="utf-8") for path in files)


def load_yaml(path: Path) -> Any:
    return yaml.safe_load(path.read_text(encoding="utf-8"))


def role_vars(role: str, kind: str = "defaults") -> dict[str, Any]:
    """Return a role's parsed defaults/main.yml or vars/main.yml."""

    path = role_path(role) / kind / "main.yml"
    return (load_yaml(path) or {}) if path.exists() else {}


def role_dependencies(role: str) -> list[str]:
    """Return the role names listed in a role's meta dependencies."""

    path = role_path(role) / "meta" / "main.yml"
    if not path.exists():
        return []
    entries = (load_yaml(path) or {}).get("dependencies") or []
    return [entry["role"] if isinstance(entry, dict) else entry for entry in entries]


def _walk(entries: list[dict[str, Any]] | None, directory: Path) -> Iterator[dict[str, Any]]:
    for entry in entries or []:
        yield entry
        for section in _BLOCK_SECTIONS:
            if section in entry:
                yield from _walk(entry[section], directory)
        for action in _INCLUDE_ACTIONS:
            target = entry.get(action)
            if isinstance(target, dict):
                target = target.get("file")
            if isinstance(target, str) and "{{" not in target:
                yield from _walk(load_yaml(directory / target), directory)


def tasks(role: str, file_name: str = "main.yml") -> list[dict[str, Any]]:
    """Return a role's tasks flattened through blocks and static includes."""

    directory = role_path(role) / "tasks"
    return list(_walk(load_yaml(directory / file_name), directory))


def playbook_tasks(relative: str) -> list[dict[str, Any]]:
    """Return every play-level and task-level entry of a playbook."""

    path = PLAYBOOKS / relative
    flattened: list[dict[str, Any]] = []
    for play in load_yaml(path) or []:
        flattened.append(play)
        for section in ("pre_tasks", "tasks", "post_tasks", "handlers"):
            flattened.extend(_walk(play.get(section), path.parent))
    return flattened


def find_task(entries: list[dict[str, Any]], name: str) -> dict[str, Any]:
    matches = [entry for entry in entries if entry.get("name") == name]
    if len(matches) != 1:
        raise LookupError(f"expected exactly one task named {name!r}, found {len(matches)}")
    return matches[0]


def task(role: str, name: str, file_name: str = "main.yml") -> dict[str, Any]:
    """Return the single parsed task named ``name`` in a role."""

    return find_task(tasks(role, file_name), name)


def runs_before(role: str, earlier: str, later: str, file_name: str = "main.yml") -> bool:
    """Return whether task ``earlier`` precedes task ``later`` in a role."""

    names = [entry.get("name") for entry in tasks(role, file_name)]
    for name in (earlier, later):
        if names.count(name) != 1:
            raise LookupError(f"expected exactly one task named {name!r}")
    return names.index(earlier) < names.index(later)


def enclosing_block(role: str, name: str, file_name: str = "main.yml") -> dict[str, Any]:
    """Return the innermost block whose block section contains task ``name``."""

    directory = role_path(role) / "tasks"
    matches = [
        entry
        for entry in tasks(role, file_name)
        if "block" in entry
        and any(child.get("name") == name for child in _walk(entry["block"], directory))
    ]
    if not matches:
        raise LookupError(f"task {name!r} is not inside a block")
    return matches[-1]


def module_args(entry: dict[str, Any], module: str) -> Any:
    """Return a task's module arguments, accepting short or FQCN names."""

    short = module.rsplit(".", 1)[-1]
    for key in (module, f"ansible.builtin.{short}", short):
        if key in entry:
            return entry[key]
    raise KeyError(f"task {entry.get('name')!r} does not use {module}")
