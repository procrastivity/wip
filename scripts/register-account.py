#!/usr/bin/env python3
"""Interactively register/verify a WIP Cloud account; Python 3 + curl only."""

import argparse
import getpass
import http.cookiejar
import http.cookies
import json
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request
import warnings


class Failure(Exception):
    pass


class TransportFailure(Failure):
    pass


def origin_url(raw):
    try:
        parsed = urllib.parse.urlsplit(raw)
        valid = (
            parsed.scheme in ("http", "https")
            and parsed.hostname
            and parsed.port != 0
            and parsed.username is None
            and parsed.password is None
            and parsed.path in ("", "/")
            and not parsed.query
            and not parsed.fragment
            and not re.search(r"[\s\\\x00-\x1f\x7f]", raw)
            and raw.isascii()
        )
    except ValueError:
        valid = False
    if not valid:
        raise argparse.ArgumentTypeError("use an HTTP(S) origin, without credentials, path or query")
    return raw.rstrip("/")


def confirm(prompt):
    return input(prompt + " [y/N]: ").strip().lower() in ("y", "yes")


def secret(prompt):
    # Never fall back to getpass's echoed, piped-input mode.
    if not sys.stdin.isatty():
        raise Failure("A terminal is required for hidden password and verification-link input.")
    with warnings.catch_warnings():
        warnings.simplefilter("error", getpass.GetPassWarning)
        try:
            return getpass.getpass(prompt)
        except getpass.GetPassWarning:
            raise Failure("Cannot disable terminal echo; no secret was read.") from None


def config_quote(value):
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"').replace(
        "\r", "\\r"
    ).replace("\n", "\\n").replace("\t", "\\t") + '"'


class Session:
    def __init__(self, origin, directory):
        self.origin = origin
        self.jar = directory / "cookies"
        self.headers = directory / "headers"
        self.body = directory / "body"
        for path in (self.jar, self.headers, self.body):
            path.touch(mode=0o600)
        self.needs_logout = False

    def request(self, method, path, payload=None):
        url = self.origin + path
        settings = ["url = " + config_quote(url)]
        headers = {"Accept": "application/json", "Origin": self.origin,
                   "Referer": self.origin + "/"}
        jar = http.cookiejar.MozillaCookieJar(str(self.jar))
        if self.jar.stat().st_size:
            jar.load(ignore_discard=True, ignore_expires=True)
            for cookie in jar:
                # curl writes session cookies with expiry 0; Python treats 0
                # as expired rather than the Netscape format's session marker.
                if cookie.expires == 0:
                    cookie.expires = None
            request = urllib.request.Request(url)
            jar.add_cookie_header(request)
            cookies = http.cookies.SimpleCookie(request.get_header("Cookie", ""))
            if "XSRF-TOKEN" in cookies:
                headers["X-XSRF-TOKEN"] = urllib.parse.unquote(cookies["XSRF-TOKEN"].value)
        if payload is not None:
            headers["Content-Type"] = "application/json"
            settings.append("data-binary = " + config_quote(json.dumps(payload)))
        settings.extend("header = " + config_quote(k + ": " + v) for k, v in headers.items())
        # Disable ~/.curlrc, redirects and retries. Secrets travel through stdin,
        # never argv, environment variables or diagnostic output.
        result = subprocess.run(
            ["curl", "--disable", "--silent", "--show-error", "--globoff", "--config", "-",
             "--request", method, "--connect-timeout", "10", "--max-time", "30",
             "--proto", "=http,https", "--cookie", str(self.jar),
             "--cookie-jar", str(self.jar), "--dump-header", str(self.headers),
             "--output", str(self.body), "--write-out", "%{http_code}"],
            input="\n".join(settings) + "\n", text=True, capture_output=True,
        )
        if result.returncode:
            raise TransportFailure(
                f"Request failed (curl exit {result.returncode}); no automatic retry. "
                "A submitted operation may have completed. Check the endpoint, TLS and connectivity."
            )
        try:
            status = int(result.stdout)
        except ValueError:
            raise TransportFailure("Invalid curl result; operation outcome is unconfirmed.") from None
        return status

    def data(self):
        try:
            value = json.loads(self.body.read_bytes())
        except (ValueError, UnicodeError):
            raise Failure("Expected JSON, received an incompatible response (body withheld).") from None
        if not isinstance(value, dict):
            raise Failure("Expected a JSON object (body withheld).")
        return value

    def expect(self, status, expected, operation):
        print(f"{operation} HTTP {status}")
        if status in expected:
            return
        hints = {
            401: "Session absent or expired. Restart in existing-account mode.",
            403: "Access denied or verification link invalid/expired; do not bypass security settings.",
            419: "CSRF/session expired. Restart in existing-account mode to obtain fresh cookies.",
            422: "Validation failed. Check your inputs; an account may already exist.",
            429: "Rate limited. Wait before retrying; no automatic resend.",
        }
        hint = hints.get(status, "Unexpected response; no redirect or automatic retry was attempted.")
        if status == 429:
            match = re.search(r"(?im)^Retry-After:\s*(\d+)\s*$", self.headers.read_text())
            if match:
                hint += f" Retry-After: {match[1]} seconds."
        raise Failure(hint)

    def csrf(self):
        self.expect(self.request("GET", "/sanctum/csrf-cookie"), (204,), "CSRF")

    def login(self, email):
        self.csrf()
        self.needs_logout = True  # Also clean up after a lost login response.
        status = self.request("POST", "/login", {"email": email, "password": secret("Password: ")})
        self.expect(status, (200, 204), "Session login")

    def register(self, email):
        while True:
            name = input("Name: ").strip()
            password = secret("New password: ")
            if password != secret("Confirm password: "):
                print("Passwords differ; nothing submitted.")
                continue
            if not confirm(f"Create account {email} at {self.origin}?"):
                raise Failure("Cancelled; account registration not submitted.")
            self.csrf()
            self.needs_logout = True
            try:
                status = self.request("POST", "/register", {
                    "name": name, "email": email, "password": password,
                    "password_confirmation": password,
                })
            except TransportFailure as error:
                print(str(error))
                status = None
            finally:
                password = None
            if status == 201:
                print("Registration HTTP 201; account created. Do not register again.")
                return
            if status == 422:
                data = self.data()  # Reject proxy HTML instead of assuming Laravel validation.
                print("Registration HTTP 422; validation failed or this email already exists.")
                errors = data.get("errors")
                if isinstance(errors, dict):
                    # Never echo arbitrary response text that might reflect a secret.
                    fields = [field for field in ("name", "email", "password") if field in errors]
                    print("Check fields: " + ", ".join(fields) + ". Restart to change the email.")
                action = input("Correct inputs, log in to an existing account, or stop? [edit/login/stop]: ").strip()
                if action == "edit":
                    continue
                if action != "login":
                    raise Failure("Stopped; existing accounts were not changed.")
            elif status is None or status >= 500 or 200 <= status < 300:
                print("Registration outcome unconfirmed; it will not be repeated automatically.")
                if not confirm("Try existing-account login to reconcile this email?"):
                    raise Failure("Stopped. Resume with existing-account mode; do not blindly register again.")
            else:
                self.expect(status, (201,), "Registration")
            self.login(email)
            return

    def account(self, email):
        status = self.request("GET", "/api/me")
        print(f"Account HTTP {status}")
        if status == 403:
            return False
        self.expect(status, (200,), "Account check")
        data = self.data()
        if (str(data.get("email", "")).lower() != email
                or not data.get("id") or not data.get("email_verified_at")
                or not isinstance(data.get("personal_organization"), dict)
                or not data["personal_organization"].get("id")):
            raise Failure("Account identity, verification or personal organization check failed.")
        print("Expected account: True; Email verified: True; Personal organization present: True")
        return True

    def verify(self, email):
        print("Retrieve the verification link from your inbox, or application logs for log-only mail.")
        print("Paste it at the hidden prompt. Enter 'resend' explicitly, or 'login' if the session expired.")
        while True:
            link = secret("Verification link (hidden; resend/login/quit): ").strip()
            if link == "quit":
                raise Failure("Stopped; account retained. Resume in existing-account mode.")
            if link == "login":
                self.login(email)
                if self.account(email):
                    return
                continue
            if link == "resend":
                status = self.request("POST", "/email/verification-notification", {})
                self.expect(status, (202, 204), "Verification notification")
                if status == 204 and self.account(email):
                    return
                print("Wait for the verification link; notification acceptance is not verification.")
                continue
            try:
                parsed = urllib.parse.urlsplit(link)
            except ValueError:
                print("Link rejected: invalid URL. No request sent.")
                continue
            if (parsed.scheme + "://" + parsed.netloc != self.origin
                    or parsed.fragment or not link.isascii()
                    or re.search(r"[\s\\\x00-\x1f\x7f]", link)
                    or not re.fullmatch(r"/email/verify/[0-9]+/[a-fA-F0-9]{40}", parsed.path)
                    or not parsed.query):
                print("Link rejected: expected a signed verification link on this exact origin. No request sent.")
                continue
            status = self.request("GET", parsed.path + "?" + parsed.query)
            print(f"Verification HTTP {status}")
            if status == 403:
                print("Link denied/expired or access blocked. Check the link; use 'resend' explicitly if needed.")
                continue
            if status in (401, 419):
                print("Session expired. Enter 'login' to resume without registering again.")
                continue
            self.expect(status, (204,), "Verification check")
            if self.account(email):
                return

    def close(self):
        if not self.needs_logout:
            return
        try:
            self.expect(self.request("POST", "/logout", {}), (204,), "Session logout")
        except (Failure, OSError, KeyboardInterrupt):
            print("Warning: server logout unconfirmed; local temporary files will still be removed.", file=sys.stderr)


def connect_cli(origin, email):
    if not shutil.which("wip"):
        print("wip is not on PATH; registration complete. Run named CLI login after installing it.")
        return
    if not confirm("Also log your normal wip CLI into this endpoint?"):
        return
    result = subprocess.run(["wip", "--json", "plumbing", "sync", "connections"],
                            capture_output=True, text=True)
    if result.returncode:
        raise Failure("Cannot inspect saved CLI connections; they were left unchanged.")
    try:
        connections = json.loads(result.stdout)
        if not isinstance(connections, list) or any(not isinstance(c, dict) for c in connections):
            raise ValueError()
    except ValueError:
        raise Failure("Unexpected CLI connection list; saved connections were left unchanged.") from None
    while True:
        name = input("New local connection name (e.g. work; blank skips): ").strip()
        if not name:
            return
        if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]*", name):
            print("Use letters, digits, underscores or hyphens, starting with a letter or digit.")
            continue
        if any(c.get("name") == name for c in connections):
            print("That name is already saved; it will not be overwritten. Choose another or skip.")
            continue
        print("wip will ask for your password again and store its own protected token.")
        result = subprocess.run(["wip", "plumbing", "sync", "login", "--connection", name,
                                 "--server", origin, "--email", email])
        if result.returncode:
            raise Failure("Account verified, but CLI login did not complete; inspect wip's message before retrying.")
        return


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("endpoint", type=origin_url, help="WIP Cloud origin, e.g. https://wip.example.com")
    args = parser.parse_args()
    if not sys.stdin.isatty():
        raise Failure("Run this interactive helper in a terminal; do not pipe passwords into it.")
    if not shutil.which("curl"):
        raise Failure("curl is required on PATH (no Python packages are needed).")
    print(f"Endpoint: {args.endpoint}")
    if args.endpoint.startswith("http:"):
        print("WARNING: HTTP does not encrypt credentials. Use only for an explicitly trusted disposable local test.")
        if not confirm("Continue over unencrypted HTTP?"):
            return
    with tempfile.TemporaryDirectory(prefix="wip-registration-") as directory:
        session = Session(args.endpoint, Path(directory))
        try:
            session.expect(session.request("GET", "/up"), (200,), "Health")
            mode = input("New account or resume an existing account? [new/existing]: ").strip() or "new"
            if mode not in ("new", "existing"):
                raise Failure("Choose new or existing; nothing submitted.")
            email = input("Email: ").strip().lower()
            if not email:
                raise Failure("Email is required; nothing submitted.")
            if mode == "existing":
                session.login(email)
            else:
                session.register(email)
            if not session.account(email):
                session.verify(email)
            print(f"Account verified at {args.endpoint}. No projects paired or uploaded.")
            connect_cli(args.endpoint, email)
        finally:
            session.close()
    print("Temporary registration files removed.")


if __name__ == "__main__":
    def interrupt(_signum, _frame):
        raise KeyboardInterrupt()

    signal.signal(signal.SIGINT, interrupt)
    signal.signal(signal.SIGTERM, interrupt)
    try:
        main()
    except (Failure, OSError) as error:
        # OSError may contain private paths but never request bodies or signed URLs.
        print(f"Registration helper: {error}", file=sys.stderr)
        sys.exit(1)
    except (KeyboardInterrupt, EOFError):
        print("\nStopped; temporary files removed. Any created account is retained; resume using existing mode.",
              file=sys.stderr)
        sys.exit(130)
