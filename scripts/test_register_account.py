"""Public-entrypoint checks; run: python3 -m unittest discover -s scripts -p 'test_*.py' -v."""

import http.server
import json
import os
from pathlib import Path
import pty
import select
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest


SCRIPT = Path(__file__).with_name("register-account.py").resolve()
PASSWORD = "synthetic-only-pa55word"
VERIFY_PATH = "/email/verify/1/" + "a" * 40 + "?expires=123&signature=test-signature"


class Terminal:
    def __init__(self, endpoint, env):
        self.fd, slave = pty.openpty()
        try:
            self.process = subprocess.Popen(
                [sys.executable, str(SCRIPT), endpoint], env=env,
                stdin=slave, stdout=slave, stderr=slave, start_new_session=True,
            )
        finally:
            os.close(slave)
        self.pid = self.process.pid
        self.output = ""
        self.position = 0
        self.status = None

    def expect(self, text, timeout=15):
        deadline = time.monotonic() + timeout
        while text not in self.output[self.position:]:
            if time.monotonic() > deadline:
                raise AssertionError(f"Timed out waiting for {text!r}; transcript: {self.output!r}")
            if select.select([self.fd], [], [], 0.1)[0]:
                try:
                    data = os.read(self.fd, 65536)
                except OSError:
                    data = b""
                if not data:
                    raise AssertionError(f"Exited before {text!r}; transcript: {self.output!r}")
                self.output += data.decode(errors="replace")
        self.position = self.output.index(text, self.position) + len(text)

    def send(self, text):
        os.write(self.fd, (text + "\n").encode())

    def answer(self, prompt, text):
        self.expect(prompt)
        self.send(text)

    def finish(self, code=0):
        deadline = time.monotonic() + 15
        while self.status is None:
            if select.select([self.fd], [], [], 0.05)[0]:
                try:
                    self.output += os.read(self.fd, 65536).decode(errors="replace")
                except OSError:
                    pass
            self.status = self.process.poll()
            if time.monotonic() > deadline:
                raise AssertionError("Helper did not exit")
        if self.status != code:
            raise AssertionError(f"Exit {self.status}, expected {code}: {self.output}")
        if PASSWORD in self.output or "signature=test-signature" in self.output:
            raise AssertionError("Secret echoed to terminal")

    def close(self):
        if self.status is None:
            self.process.kill()
            self.process.wait()
        os.close(self.fd)


class RegistrationTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="registration-test-")
        self.root = Path(self.directory.name)
        self.env = dict(os.environ, TMPDIR=str(self.root), HOME=str(self.root),
                        XDG_CONFIG_HOME=str(self.root / "config"),
                        PATH="/usr/bin:/bin", PYTHONDONTWRITEBYTECODE="1")
        self.calls = []
        self.registered = False
        self.verified = False
        self.drop_register = False
        self.health_status = 200
        self.verify_status = 204
        self.account_payload = None
        self.account_html = False
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def handle_request(self):
                payload = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                owner.calls.append((self.command, self.path, dict(self.headers), payload))
                status, body = 200, {}
                if self.path == "/up":
                    status = owner.health_status
                elif self.path == "/sanctum/csrf-cookie":
                    status = 204
                elif self.path == "/register":
                    if owner.registered:
                        status, body = 422, {"errors": {"email": ["Already registered"]}}
                    else:
                        owner.registered = True
                        status = 201
                    if owner.drop_register:
                        self.connection.shutdown(socket.SHUT_RDWR)
                        self.connection.close()
                        return
                elif self.path == "/login":
                    pass
                elif self.path == "/api/me":
                    status = 200 if owner.verified else 403
                    body = owner.account_payload or {
                        "id": 1, "email": "person@example.test", "email_verified_at": "2026-11-24",
                        "personal_organization": {"id": "personal-test"},
                    }
                elif self.path.startswith("/email/verify/"):
                    status = owner.verify_status
                    if status == 204:
                        owner.verified = True
                elif self.path == "/email/verification-notification":
                    status = 202
                elif self.path == "/logout":
                    status = 204
                else:
                    status = 404
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                if self.path == "/sanctum/csrf-cookie":
                    self.send_header("Set-Cookie", "XSRF-TOKEN=fixture%3D; Path=/")
                if status == 302:
                    self.send_header("Location", "http://127.0.0.1:1/should-not-follow")
                if status == 429:
                    self.send_header("Retry-After", "120")
                self.end_headers()
                if status not in (204, 302):
                    if self.path == "/api/me" and owner.account_html:
                        self.wfile.write(b"<html>not a Laravel API response</html>")
                    else:
                        self.wfile.write(json.dumps(body).encode())

            do_GET = handle_request
            do_POST = handle_request

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.origin = f"http://127.0.0.1:{self.server.server_port}"
        self.terminals = []

    def tearDown(self):
        for terminal in self.terminals:
            terminal.close()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.directory.cleanup()

    def terminal(self):
        terminal = Terminal(self.origin, self.env)
        self.terminals.append(terminal)
        terminal.answer("Continue over unencrypted HTTP?", "y")
        return terminal

    def register(self):
        terminal = self.terminal()
        terminal.answer("[new/existing]:", "new")
        terminal.answer("Email:", "person@example.test")
        terminal.answer("Name:", "Example Person")
        terminal.answer("New password:", PASSWORD)
        terminal.answer("Confirm password:", PASSWORD)
        terminal.answer("Create account", "y")
        return terminal

    def assert_clean(self):
        self.assertEqual([], list(self.root.glob("wip-registration-*")))

    def test_registration_resend_link_guards_and_cleanup(self):
        terminal = self.register()
        prompt = "Verification link (hidden; resend/login/quit):"
        terminal.answer(prompt, "https://other.example.test" + VERIFY_PATH)
        terminal.expect("Link rejected:")
        terminal.answer(prompt, "http://[invalid")
        terminal.expect("Link rejected:")
        terminal.answer(prompt, "resend")
        terminal.expect("Verification notification HTTP 202")
        terminal.answer(prompt, self.origin + VERIFY_PATH)
        terminal.finish()
        self.assertIn("Expected account: True", terminal.output)
        self.assertIn("Session logout HTTP 204", terminal.output)
        self.assertEqual(1, sum(path == VERIFY_PATH for _, path, _, _ in self.calls))
        registration = next(call for call in self.calls if call[1] == "/register")
        self.assertEqual("fixture=", registration[2]["X-XSRF-TOKEN"])
        self.assertEqual(PASSWORD, json.loads(registration[3])["password"])
        self.assert_clean()

    def test_lost_registration_response_reconciles_without_replay(self):
        self.drop_register = True
        self.verified = True
        terminal = self.register()
        terminal.answer("Try existing-account login", "y")
        terminal.answer("Password:", PASSWORD)
        terminal.finish()
        self.assertEqual(1, sum(path == "/register" for _, path, _, _ in self.calls))
        self.assertTrue(any(path == "/login" for _, path, _, _ in self.calls))
        self.assert_clean()

    def test_duplicate_registration_can_resume_without_recreating(self):
        self.registered = self.verified = True
        terminal = self.register()
        terminal.answer("[edit/login/stop]:", "login")
        terminal.answer("Password:", PASSWORD)
        terminal.finish()
        self.assertIn("Registration HTTP 422", terminal.output)
        self.assert_clean()

    def test_expired_link_and_interrupt_preserve_account_and_remove_files(self):
        self.verify_status = 403
        terminal = self.register()
        prompt = "Verification link (hidden; resend/login/quit):"
        terminal.answer(prompt, self.origin + VERIFY_PATH)
        terminal.expect("Verification HTTP 403")
        terminal.expect(prompt)
        paths = list(self.root.glob("wip-registration-*"))
        self.assertEqual(1, len(paths))
        self.assertEqual(0o700, paths[0].stat().st_mode & 0o777)
        for path in paths[0].iterdir():
            self.assertEqual(0o600, path.stat().st_mode & 0o777)
        os.kill(terminal.pid, signal.SIGINT)
        terminal.finish(130)
        self.assertTrue(self.registered)
        self.assert_clean()

    def test_session_expiry_can_login_and_resume_without_registering_again(self):
        for status in (401, 419):
            with self.subTest(status=status):
                self.registered = False
                self.verified = False
                self.verify_status = status
                terminal = self.register()
                prompt = "Verification link (hidden; resend/login/quit):"
                terminal.answer(prompt, self.origin + VERIFY_PATH)
                terminal.expect("Session expired. Enter 'login'")
                self.verify_status = 204
                terminal.answer(prompt, "login")
                terminal.answer("Password:", PASSWORD)
                terminal.answer(prompt, self.origin + VERIFY_PATH)
                terminal.finish()
                self.assertIn("Expected account: True", terminal.output)
                self.assert_clean()
        self.assertEqual(2, sum(path == "/register" for _, path, _, _ in self.calls))

    def test_unexpected_redirect_throttle_and_wrong_identity_never_succeed(self):
        for status in (302, 429):
            with self.subTest(status=status):
                self.health_status = status
                terminal = self.terminal()
                terminal.finish(1)
                self.assertNotIn("Email:", terminal.output)
                self.assert_clean()
        self.health_status = 200
        self.verified = True
        self.account_payload = {"id": 2, "email": "wrong@example.test",
                                "email_verified_at": "2026", "personal_organization": {"id": "wrong"}}
        terminal = self.register()
        terminal.finish(1)
        self.assertIn("Account identity, verification or personal organization check failed", terminal.output)
        self.assert_clean()
        self.account_html = True
        terminal = self.terminal()
        terminal.answer("[new/existing]:", "existing")
        terminal.answer("Email:", "person@example.test")
        terminal.answer("Password:", PASSWORD)
        terminal.finish(1)
        self.assertIn("Expected JSON", terminal.output)
        self.assert_clean()


if __name__ == "__main__":
    unittest.main()
