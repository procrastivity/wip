"""Drive the real CLI's hidden input on a POSIX PTY; only dummy test secrets."""

import errno
import json
import os
import select
import subprocess
import sys
import termios
import time

password = json.load(sys.stdin)
master, slave = os.openpty()
initial = termios.tcgetattr(slave)
process = subprocess.Popen(
    sys.argv[1:], stdin=slave, stdout=slave, stderr=slave, start_new_session=True
)
output = bytearray()
sent = False
deadline = time.monotonic() + 10
try:
    while True:
        if time.monotonic() > deadline:
            raise TimeoutError("terminal login did not complete")
        if select.select([master], [], [], 0.01)[0]:
            try:
                chunk = os.read(master, 65536)
            except OSError as error:
                if error.errno != errno.EIO:
                    raise
                chunk = b""
            output.extend(chunk)
        if not sent and b"Password: " in output:
            # The prompt can arrive just before ReadPassword disables echo.
            # Wait for the actual terminal setting, not an arbitrary sleep.
            if not termios.tcgetattr(slave)[3] & termios.ECHO:
                os.write(master, password.encode() + b"\n")
                sent = True
        if process.poll() is not None:
            while select.select([master], [], [], 0)[0]:
                output.extend(os.read(master, 65536))
            json.dump(
                {"output": output.decode(), "exit_code": process.returncode,
                 "hidden_input": sent, "restored": termios.tcgetattr(slave) == initial},
                sys.stdout,
            )
            break
finally:
    if process.poll() is None:
        process.kill()
    process.wait()
    os.close(slave)
    os.close(master)
