"""Shared process ownership for QA-008 runners (Linux test environment)."""
import os
import signal
import subprocess


def bounded_run(command, *, timeout, capture_output=True, **kwargs):
    """Like run(), but a timeout kills the complete owned process group."""
    if capture_output:
        kwargs.update(stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    process = subprocess.Popen(command, start_new_session=True, **kwargs)
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        stdout, stderr = process.communicate()
        raise subprocess.TimeoutExpired(command, timeout, output=stdout, stderr=stderr) from None
    return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)
