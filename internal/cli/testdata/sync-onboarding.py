"""Drive actual onboarding choices on a POSIX terminal, using dummy fixtures."""

import errno
import json
import os
import select
import subprocess
import sys
import time

steps = json.load(sys.stdin)
master, slave = os.openpty()
process = subprocess.Popen(sys.argv[1:], stdin=slave, stdout=slave, stderr=slave)
os.close(slave)
output = bytearray()
cursor = 0
deadline = time.monotonic() + 20
try:
    while True:
        if time.monotonic() > deadline:
            raise TimeoutError(output.decode(errors="replace"))
        if select.select([master], [], [], 0.02)[0]:
            try:
                chunk = os.read(master, 65536)
            except OSError as error:
                if error.errno != errno.EIO:
                    raise
                chunk = b""
            output.extend(chunk)
        if steps:
            prompt, answer = steps[0]
            found = output.find(prompt.encode(), cursor)
            if found >= 0:
                cursor = found + len(prompt.encode())
                os.write(master, (answer + "\n").encode())
                steps.pop(0)
        if process.poll() is not None:
            while select.select([master], [], [], 0)[0]:
                try:
                    chunk = os.read(master, 65536)
                except OSError as error:
                    if error.errno != errno.EIO:
                        raise
                    break
                if not chunk:
                    break
                output.extend(chunk)
            json.dump({"output": output.decode(), "exit_code": process.returncode,
                       "remaining_steps": len(steps)}, sys.stdout)
            break
finally:
    if process.poll() is None:
        process.kill()
    process.wait()
    os.close(master)
