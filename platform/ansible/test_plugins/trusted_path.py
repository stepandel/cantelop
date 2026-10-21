"""Jinja tests for stat results that must be trusted, nonsymlinked paths.

Roles stat a path with ``follow: false`` and then assert it is safe to read
or execute.  Use these tests in the assert instead of repeating the
exists/type/symlink/owner/mode checks:

    - name: Require the trusted control-plane token
      ansible.builtin.assert:
        that:
          - token_status.stat is trusted_file(owner='root', group='root', mode='0600')
          - token_status.stat.size | default(0) | int > 0

``owner``, ``group``, ``mode``, and ``modes`` are only checked when given:
``mode`` must equal the stat mode and ``modes`` must contain it.  A missing
stat field fails the test instead of raising an undefined-variable error.
"""

from collections.abc import Mapping


# Distinguishes "not checked" from an explicit null owner, group, or mode, so
# a null expectation fails exactly as an equality comparison would.
_UNCHECKED = object()


def _trusted(stat, kind, owner, group, mode, modes):
    if not isinstance(stat, Mapping):
        return False
    if not stat.get("exists", False) or not stat.get(kind, False):
        return False
    if stat.get("islnk", False):
        return False
    if owner is not _UNCHECKED and stat.get("pw_name", "") != owner:
        return False
    if group is not _UNCHECKED and stat.get("gr_name", "") != group:
        return False
    if mode is not _UNCHECKED and stat.get("mode", "") != mode:
        return False
    if modes is not _UNCHECKED and stat.get("mode", "") not in modes:
        return False
    return True


def trusted_file(stat, owner=_UNCHECKED, group=_UNCHECKED, mode=_UNCHECKED, modes=_UNCHECKED):
    """Return whether a stat result is an existing nonsymlinked regular file."""

    return _trusted(stat, "isreg", owner, group, mode, modes)


def trusted_directory(stat, owner=_UNCHECKED, group=_UNCHECKED, mode=_UNCHECKED, modes=_UNCHECKED):
    """Return whether a stat result is an existing nonsymlinked directory."""

    return _trusted(stat, "isdir", owner, group, mode, modes)


class TestModule:
    def tests(self):
        return {
            "trusted_file": trusted_file,
            "trusted_directory": trusted_directory,
        }
