"""Print wall time for every Ansible task, longest first."""

from time import monotonic

from ansible.plugins.callback import CallbackBase

DOCUMENTATION = """
name: task_timings
type: aggregate
short_description: Print task wall times after a playbook run
description:
  - Reports every task's controller wall time, longest first.
requirements:
  - enable in callbacks_enabled
"""


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "aggregate"
    CALLBACK_NAME = "task_timings"
    CALLBACK_NEEDS_ENABLED = True

    def __init__(self):
        super().__init__()
        self._started = None
        self._task = None
        self._durations = []

    def _finish_task(self):
        if self._started is not None:
            self._durations.append((monotonic() - self._started, self._task))

    def v2_playbook_on_task_start(self, task, is_conditional):
        self._finish_task()
        self._started = monotonic()
        self._task = task.get_name().strip()

    def v2_playbook_on_handler_task_start(self, task):
        self.v2_playbook_on_task_start(task, False)

    def v2_playbook_on_stats(self, stats):
        self._finish_task()
        self._display.display("\nTASK WALL TIMES (longest first)")
        for duration, name in sorted(self._durations, reverse=True):
            self._display.display(f"{duration:8.1f}s  {name}")
