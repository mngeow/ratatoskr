#!/usr/bin/env python3
"""A reference adapter for the Ratatoskr participant API (contract 1.0).

UNTESTED: this program has never been run - not by its author, not by the team. It is an example of
the contract in a language other than Go, not a claim that any harness works. Read it, then run it
yourself.

It does four things and nothing else:

  pair      read the discovery document, redeem the one-time pairing code from a file or the
            environment, and write the returned credential into a file the user names, with mode 0600
  register  register one session name
  poll      long-poll the inbox, print every offer, and acknowledge it saved:true
  send      send one message to a full name@host address and report the gateway's result honestly

Every connection is pinned: over HTTPS the certificate's DER SHA-256 is compared before a single byte of
HTTP is written, on every new connection, with no proxy, no redirect and no fallback. Plain HTTP needs
--insecure-http and warns on every start.

What this adapter will not do, on purpose:

  * it never puts the credential or the pairing code on the command line, in a log line or in an error;
  * it never retries a send: an answer that did not arrive, a body it cannot parse, a 5xx or a result
    word it does not know is reported as UNCERTAIN, never as failed;
  * it never describes host_stored as delivered or read: it means the other side has the message.

Python 3.8 or newer, standard library only.
"""

import argparse
import errno
import hashlib
import hmac
import http.client
import json
import os
import re
import socket
import ssl
import sys
import time
import unicodedata
import uuid
from urllib.parse import urlsplit, urlencode

CONTRACT = "1.0"
CONTRACT_MAJOR = "1"
CONTRACT_HEADER = "X-Ratatoskr-Contract"
API_PREFIX = "/participant/v1"

# Timeouts. The poll must outlast the gateway's 20 s wait and a send must outlast its 8 s SendWait,
# which is why they are not the same number.
POLL_WAIT_SECONDS = 20
POLL_TIMEOUT_SECONDS = 25
SEND_TIMEOUT_SECONDS = 10
REQUEST_TIMEOUT_SECONDS = 10

# Limits from the contract. The body limit is in bytes, so it is checked on the encoded value.
MAX_BODY_BYTES = 65536
MAX_SECRET_BYTES = 4096  # the pairing code and the credential are both tiny; nothing bigger is one

SESSION_NAME = re.compile(r"^[a-z0-9_-]{1,32}$")
PARTICIPANT_ID = re.compile(r"^[a-z0-9-]{1,63}$")
ADDRESS = re.compile(r"^[a-z0-9_-]{1,32}@[a-z0-9-]{1,63}$")
MESSAGE_ID = re.compile(r"^[A-Za-z0-9-]{1,64}$")
PIN_HEX = re.compile(r"^sha256:([0-9a-fA-F]{64})$")
PIN_BARE_HEX = re.compile(r"^([0-9a-fA-F]{64})$")

# Exit codes. 0 and 1 and 3 are about one request's outcome; 4 and 5 are about the poll loop.
EXIT_OK = 0
EXIT_FAILED = 1  # the gateway refused it, decided it was not delivered, or it provably was not sent
EXIT_USAGE = 2  # this adapter's own arguments, files or configuration
EXIT_UNCERTAIN = 3  # the outcome is unknown: it may have been stored
EXIT_REPLACED = 4  # another poll displaced this one
EXIT_REVOKED = 5  # the credential is revoked or unknown

# The six result words the gateway produces. Anything else is uncertain, never failed.
RESULT_MEANINGS = {
    "host_stored": "host_stored: the other side saved it; delivery and read are not confirmed",
    "session_gone": "session_gone: the recipient session is gone; it was not saved",
    "unknown_recipient": "unknown_recipient: no agent has that address",
    "destination_unreachable": "destination_unreachable: the destination was not reachable; it was not queued",
    "delivery_uncertain": "delivery_uncertain: it may have been saved",
    "gateway_uncertain": "gateway_uncertain: the first send with this id is still in progress",
}
DECIDED_RESULTS = frozenset(RESULT_MEANINGS)
UNCERTAIN_RESULTS = frozenset({"delivery_uncertain", "gateway_uncertain"})


class AdapterError(Exception):
    """A failure this adapter reports as one line and an exit code."""

    def __init__(self, message, code=EXIT_USAGE):
        super().__init__(message)
        self.message = message
        self.code = code


class PinMismatch(AdapterError):
    """The certificate was not the pinned one. Nothing was sent."""


class ConnectFailed(AdapterError):
    """No connection was established. Nothing was sent."""


class Ambiguous(Exception):
    """The request may or may not have reached the gateway."""


def credential_refused():
    """The one failure for a revoked or unknown credential. It never says which."""
    return AdapterError(
        "the gateway refused this credential: it is revoked or unknown (401 unauthorized)", EXIT_REVOKED
    )


def warn(message):
    """Write one terminal-safe line to stderr."""
    sys.stderr.write(terminal_text(message) + "\n")
    sys.stderr.flush()


def fail(message, code=EXIT_USAGE):
    """Report a failure and stop."""
    raise AdapterError(message, code)


# --------------------------------------------------------------------------------------------------
# Endpoint, pin and connection
# --------------------------------------------------------------------------------------------------


class Endpoint(object):
    """Where the participant API is and what its certificate must hash to."""

    def __init__(self, scheme, host, port, pin):
        self.scheme = scheme
        self.host = host
        self.port = port
        self.pin = pin  # None only for plain HTTP

    def describe(self):
        """A line safe to print: no query, no credential, no pin."""
        host = self.host
        if ":" in host and not host.startswith("["):
            host = "[" + host + "]"  # an IPv6 literal needs brackets to be unambiguous
        return "%s://%s:%d%s" % (self.scheme, terminal_text(host), self.port, API_PREFIX)


def parse_pin(value):
    """Accept sha256:<64 hex> or a bare 64 hex string; refuse everything else."""
    if value is None:
        fail("--pin is missing: HTTPS needs the certificate SHA-256 that 'rtk gateway participant pair' prints")
    text = value.strip()
    match = PIN_HEX.match(text) or PIN_BARE_HEX.match(text)
    if match is None:
        fail(
            "--pin must be the certificate SHA-256 as sha256:<64 hex digits>; "
            "the sha256//... SPKI pin is a different rule for key-pinning clients and is not accepted here"
        )
    return match.group(1).lower()


def make_endpoint(url, pin_value, insecure_http):
    """Turn --url and --pin into an Endpoint, refusing every combination the contract does not allow."""
    if not url:
        fail("--url is missing: use the participant API URL that 'rtk gateway participant pair' prints")

    try:
        parts = urlsplit(url.strip())
    except ValueError:
        fail("--url is malformed")
    scheme = parts.scheme.lower()
    if scheme not in ("http", "https"):
        fail("--url must start with http:// or https://")
    if parts.username is not None or parts.password is not None:
        fail("--url must not carry a user name or password")
    if parts.query or parts.fragment:
        fail("--url must be the base URL only: no query and no fragment")
    if parts.path not in ("", "/"):
        fail("--url must be the base URL only: no path (the adapter builds /participant/v1 itself)")

    host = parts.hostname
    if not host:
        fail("--url has no host")
    try:
        port = parts.port
    except ValueError:
        fail("--url has a port that is not a number")
    if port is None:
        port = 443 if scheme == "https" else 80

    if scheme == "https":
        if pin_value:
            return Endpoint(scheme, host, port, parse_pin(pin_value))
        return Endpoint(scheme, host, port, parse_pin(None))

    # Plain HTTP: explicit opt-in, no pin, and a warning on every start.
    if pin_value:
        fail("--pin is for HTTPS only; a plain-HTTP connection has nothing to pin")
    if not insecure_http:
        fail("plain HTTP needs --insecure-http: it sends the credential readable on the network path")
    warn(
        "WARNING: --insecure-http is set. The bearer credential and message bodies are readable by "
        "anything on the network path. Use HTTPS with --pin whenever possible."
    )
    return Endpoint(scheme, host, port, None)


class PinnedHTTPSConnection(http.client.HTTPSConnection):
    """An HTTPS connection that refuses to carry a request until the certificate matches the pin.

    Verification is disabled for this socket alone, because the certificate is self-signed and carries
    no host name: the pin IS the identity. The handshake completes first, then the peer certificate's
    DER SHA-256 is compared in constant time, and only then is self.sock assigned - and
    http.client.HTTPConnection.request() writes nothing until its own connect() call has returned
    through this method. A fresh instance is used per request, so every connection is checked again.
    """

    def __init__(self, host, port, pin, timeout):
        self._pin_hex = pin
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        context.minimum_version = ssl.TLSVersion.TLSv1_3  # as the spec's Python recipe does
        context.check_hostname = False  # the pin is the identity; there is no name to check
        context.verify_mode = ssl.CERT_NONE  # only on this dedicated pinning socket
        http.client.HTTPSConnection.__init__(self, host, port, context=context, timeout=timeout)

    def connect(self):
        try:
            raw = socket.create_connection((self.host, self.port), self.timeout)
        except OSError as exc:
            raise ConnectFailed("could not connect to the gateway: %s" % describe_os_error(exc))

        tls_sock = None
        try:
            tls_sock = self._context.wrap_socket(raw)
            der = tls_sock.getpeercert(binary_form=True)
            actual = hashlib.sha256(der).hexdigest() if der else ""
            if not der or not hmac.compare_digest(actual, self._pin_hex):
                raise PinMismatch(
                    "the gateway certificate does not match --pin; nothing was sent "
                    "(check that --pin is the certificate SHA-256 the gateway printed)"
                )
            self.sock = tls_sock
        except BaseException:
            for sock in (tls_sock, raw):
                if sock is not None:
                    try:
                        sock.close()
                    except OSError:
                        pass
            self.sock = None
            raise


class PlainHTTPConnection(http.client.HTTPConnection):
    """A plain-HTTP connection for the explicit --insecure-http mode; nothing is pinned."""

    def __init__(self, host, port, timeout):
        http.client.HTTPConnection.__init__(self, host, port, timeout=timeout)


def describe_os_error(exc):
    """One line for an OS error, without a stack trace and without any request detail."""
    name = errno.errorcode.get(getattr(exc, "errno", None) or 0, "")
    text = getattr(exc, "strerror", None) or str(exc) or "unknown error"
    return "%s (%s)" % (text, name) if name else text


# --------------------------------------------------------------------------------------------------
# The client
# --------------------------------------------------------------------------------------------------


class Answer(object):
    """One HTTP answer, decoded as far as it can be."""

    def __init__(self, status, headers, raw_body):
        self.status = status
        self.headers = headers
        self.raw_body = raw_body
        self.body = decode_json(raw_body)

    def header(self, name):
        """A header by lower-case name, or ""."""
        return self.headers.get(name.lower(), "")

    def contract_major(self):
        """The major the gateway declared, or None when it declared nothing."""
        declared = self.header(CONTRACT_HEADER).strip()
        if not declared:
            return None
        return declared.split(".")[0]


def decode_json(raw):
    """Parse a JSON body, returning None when it is not JSON."""
    if not raw:
        return None
    try:
        return json.loads(raw.decode("utf-8"))
    except (ValueError, UnicodeDecodeError):
        return None


class Client(object):
    """The participant API client: one pinned connection per request, no retry, no redirect."""

    def __init__(self, endpoint, credential=None):
        self.endpoint = endpoint
        self.credential = credential

    def call(self, method, path, body=None, timeout=REQUEST_TIMEOUT_SECONDS, auth=True, query=None):
        """Make one request and return its Answer.

        A connection or pin failure raises ConnectFailed or PinMismatch (nothing was sent). Anything
        that fails after the request may have been written raises Ambiguous.
        """
        target = API_PREFIX + path
        if query:
            target = target + "?" + urlencode(query)

        headers = {CONTRACT_HEADER: CONTRACT, "Accept": "application/json", "Connection": "close"}
        payload = None
        if body is not None:
            payload = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        # Discovery and pair carry no credential; every other route does.
        if auth and self.credential:
            headers["Authorization"] = "Bearer " + self.credential

        connection = self._open(timeout)
        try:
            try:
                # Explicitly connect first, so a pin or connection failure is a definite "not sent"
                # rather than an ambiguous transport error.
                connection.connect()
            except AdapterError:
                raise
            except OSError as exc:
                raise ConnectFailed("could not connect to the gateway: %s" % describe_os_error(exc))

            try:
                connection.request(method, target, body=payload, headers=headers)
                response = connection.getresponse()
                raw = response.read()
            except (http.client.HTTPException, OSError, Ambiguous):
                # The request may have reached the gateway: the outcome is unknown, not a failure.
                raise Ambiguous()
            return Answer(response.status, dict((k.lower(), v) for k, v in response.getheaders()), raw)
        finally:
            try:
                connection.close()
            except (http.client.HTTPException, OSError):
                pass

    def _open(self, timeout):
        if self.endpoint.pin is not None:
            return PinnedHTTPSConnection(self.endpoint.host, self.endpoint.port, self.endpoint.pin, timeout)
        return PlainHTTPConnection(self.endpoint.host, self.endpoint.port, timeout)

    # -- operations -------------------------------------------------------------------------------

    def discover(self):
        """GET /participant/v1, no credential, and check the contract major."""
        answer = self.call("GET", "", auth=False)
        if answer.status != 200:
            fail("discovery failed: the gateway answered HTTP %d" % answer.status, EXIT_FAILED)

        major = answer.contract_major()
        if major is None:
            fail("discovery omitted the X-Ratatoskr-Contract response header", EXIT_FAILED)
        if major != CONTRACT_MAJOR:
            fail(
                "the gateway declared contract %s; this adapter speaks %s"
                % (answer.header(CONTRACT_HEADER), CONTRACT),
                EXIT_FAILED,
            )

        document = answer.body
        if not isinstance(document, dict):
            fail("discovery answered something that is not a JSON object", EXIT_FAILED)
        contract = document.get("contract")
        if not isinstance(contract, str) or contract.split(".", 1)[0] != CONTRACT_MAJOR:
            fail("discovery did not declare participant contract major %s" % CONTRACT_MAJOR, EXIT_FAILED)
        versions = document.get("versions")
        if not isinstance(versions, list) or CONTRACT_MAJOR not in [str(v) for v in versions]:
            fail(
                "the gateway does not serve participant API major %s (it serves %r)"
                % (CONTRACT_MAJOR, terminal_text(versions)),
                EXIT_FAILED,
            )
        return document

    def pair(self, code, participant=None):
        """POST /participant/v1/pair with the one-time code."""
        body = {"code": code}
        if participant:
            body["participant"] = participant
        return self.call("POST", "/pair", body=body, auth=False)

    def register(self, name):
        """POST /participant/v1/sessions."""
        return self.call("POST", "/sessions", body={"name": name})

    def poll(self, wait=POLL_WAIT_SECONDS):
        """GET /participant/v1/inbox?wait=N."""
        return self.call("GET", "/inbox", timeout=POLL_TIMEOUT_SECONDS, query={"wait": wait})

    def ack(self, message_id, saved, reason=None):
        """POST /participant/v1/messages/{id}/ack."""
        body = {"saved": bool(saved)}
        if reason:
            body["reason"] = reason
        return self.call("POST", "/messages/" + message_id + "/ack", body=body)

    def send(self, message):
        """POST /participant/v1/messages."""
        return self.call("POST", "/messages", body=message, timeout=SEND_TIMEOUT_SECONDS)


# --------------------------------------------------------------------------------------------------
# Secrets: the pair code and the credential
# --------------------------------------------------------------------------------------------------


def read_pair_code(path):
    """Read the one-time code from a file (or the environment), trimmed and never echoed."""
    text = None
    if path:
        try:
            with open(path, "rb") as handle:
                raw = handle.read(MAX_SECRET_BYTES + 1)
        except OSError as exc:
            fail("cannot read the pairing-code file: %s" % describe_os_error(exc))
        if len(raw) > MAX_SECRET_BYTES:
            fail("the pairing-code file is larger than %d bytes" % MAX_SECRET_BYTES)
        try:
            text = raw.decode("utf-8")
        except UnicodeDecodeError:
            fail("the pairing-code file is not UTF-8")
    else:
        text = os.environ.get("RATATOSKR_PAIR_CODE")

    code = (text or "").strip()
    if not code:
        fail("no pairing code: pass --pair-code-file PATH or set RATATOSKR_PAIR_CODE")
    return code


def read_credential(path):
    """Read the credential from its file, trimmed. Never printed."""
    try:
        with open(path, "rb") as handle:
            raw = handle.read(MAX_SECRET_BYTES + 1)
    except OSError as exc:
        fail("cannot read the credential file: %s" % describe_os_error(exc))
    if len(raw) > MAX_SECRET_BYTES:
        fail("the credential file is larger than %d bytes" % MAX_SECRET_BYTES)
    try:
        credential = raw.decode("utf-8").strip()
    except UnicodeDecodeError:
        fail("the credential file is not UTF-8")
    if not credential:
        fail("the credential file is empty")
    return credential


def reserve_credential_file(path):
    """Reserve the output path securely before redeeming the one-time code."""
    if os.path.lexists(path):
        fail(
            "the credential file already exists; remove it or name another path "
            "(this adapter never overwrites an owner's file)",
            EXIT_USAGE,
        )
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    try:
        descriptor = os.open(path, flags, 0o600)
    except OSError as exc:
        if getattr(exc, "errno", None) == errno.EEXIST:
            fail("the credential file already exists; remove it or name another path", EXIT_USAGE)
        fail("cannot create the credential file: %s" % describe_os_error(exc))

    try:
        if not hasattr(os, "fchmod"):
            raise OSError("this platform cannot set credential file permissions to 0600")
        os.fchmod(descriptor, 0o600)
    except OSError as exc:
        try:
            os.close(descriptor)
        except OSError:
            pass
        try:
            os.unlink(path)
        except OSError:
            pass
        fail("cannot secure the credential file: %s" % describe_os_error(exc))
    return descriptor


def write_credential(descriptor, path, credential):
    """Write a reserved mode-0600 file and remove it if the write is incomplete."""
    failure = None
    try:
        # The descriptor refers only to the new O_EXCL file, never an existing owner file.
        encoded = (credential + "\n").encode("utf-8")
        written = 0
        while written < len(encoded):
            count = os.write(descriptor, encoded[written:])
            if count <= 0:
                raise OSError("credential file write made no progress")
            written += count
        os.fsync(descriptor)
    except (OSError, AttributeError) as exc:
        failure = "cannot write the credential file: %s" % describe_os_error(exc)
    try:
        os.close(descriptor)
    except OSError as exc:
        if failure is None:
            failure = "cannot close the credential file: %s" % describe_os_error(exc)

    if failure is not None:
        try:
            os.unlink(path)  # a half-written secret is not a credential; do not leave one behind
        except OSError:
            pass
        fail(failure)


# --------------------------------------------------------------------------------------------------
# Error bodies
# --------------------------------------------------------------------------------------------------


def error_code(answer):
    """The WireError code of a refusal, or ""."""
    body = answer.body
    if isinstance(body, dict):
        code = body.get("code")
        if isinstance(code, str):
            return terminal_text(code)
    return ""


def terminal_text(value):
    """Keep ordinary Unicode visible while spelling terminal controls instead of executing them."""
    if not isinstance(value, str):
        value = str(value)
    output = []
    for char in value:
        codepoint = ord(char)
        category = unicodedata.category(char)
        if category in ("Cc", "Cf", "Cs", "Zl", "Zp"):
            if codepoint <= 0xffff:
                output.append("\\u%04x" % codepoint)
            else:
                output.append("\\U%08x" % codepoint)
        else:
            output.append(char)
    return "".join(output)


def check_refusal(answer, operation):
    """Turn a non-2xx answer into the right failure: 401 stops with 5, other 4xx with 1."""
    if answer.status == 401:
        raise credential_refused()
    code = error_code(answer)
    if 400 <= answer.status < 500:
        fail(
            "%s failed: the gateway answered HTTP %d%s" % (operation, answer.status, " " + code if code else ""),
            EXIT_FAILED,
        )
    # A 5xx or an unexpected status (this adapter never follows a redirect): the gateway may or may not
    # have acted, so the answer says uncertain, not failed.
    raise AdapterError(
        "%s: the gateway answered HTTP %d, which does not say whether it acted; treat it as uncertain"
        % (operation, answer.status),
        EXIT_UNCERTAIN,
    )


# --------------------------------------------------------------------------------------------------
# Commands
# --------------------------------------------------------------------------------------------------


def build_client(args, credential=None):
    """Build a client from the common arguments, warning once for plain HTTP."""
    endpoint = make_endpoint(args.url, args.pin, args.insecure_http)
    return Client(endpoint, credential)


def command_pair(args):
    """Discovery, then redeem the code, then write the credential file exclusively."""
    client = build_client(args)
    document = client.discover()
    print("the gateway serves participant API contract %s" % terminal_text(document.get("contract", CONTRACT)))

    code = read_pair_code(args.pair_code_file)
    participant = args.participant
    if participant is not None and not PARTICIPANT_ID.match(participant):
        fail("--participant must match ^[a-z0-9-]{1,63}$")

    # Reserve and secure the output before redeeming the one-time code, so a failed path or permission
    # check cannot consume the code without leaving a place for the credential.
    descriptor = reserve_credential_file(args.credential_file)
    try:
        try:
            answer = client.pair(code, participant)
        except Ambiguous:
            fail("the pairing request did not complete: it may or may not have been redeemed", EXIT_UNCERTAIN)
        except KeyboardInterrupt:
            fail("pairing was interrupted; the one-time code may or may not have been redeemed", EXIT_UNCERTAIN)

        if answer.status != 200:
            code_name = error_code(answer)
            if code_name == "bad_pairing_code":
                fail("the gateway refused the pairing code: it is unknown, already used or expired", EXIT_FAILED)
            check_refusal(answer, "pairing")
        document_body = answer.body
        if not isinstance(document_body, dict):
            fail("pairing answered something that is not a JSON object", EXIT_UNCERTAIN)
        credential = document_body.get("credential")
        name = document_body.get("participant")
        if not isinstance(credential, str) or not credential or not isinstance(name, str) or not name:
            fail("pairing answered without a credential or participant name", EXIT_UNCERTAIN)

        write_credential(descriptor, args.credential_file, credential)
        descriptor = None
        # The credential itself is never printed: only the participant id and the path the owner named.
        print("paired as %s; the credential is in the file you named (mode 0600)" % terminal_text(name))
        return EXIT_OK
    finally:
        if descriptor is not None:
            try:
                os.close(descriptor)
            except OSError:
                pass
            try:
                os.unlink(args.credential_file)
            except OSError:
                pass


def command_register(args):
    """Register one session by name."""
    name = args.name
    if not SESSION_NAME.match(name):
        fail("the session name must match ^[a-z0-9_-]{1,32}$")

    credential = read_credential(args.credential_file)
    client = build_client(args, credential)
    client.discover()

    try:
        answer = client.register(name)
    except Ambiguous:
        fail("the registration request did not complete: it may or may not have been registered", EXIT_UNCERTAIN)
    except KeyboardInterrupt:
        fail("registration was interrupted; the session may or may not have been registered", EXIT_UNCERTAIN)

    if answer.status != 200:
        check_refusal(answer, "registering the session")
    body = answer.body
    if not isinstance(body, dict):
        fail("registration answered something that is not a JSON object", EXIT_UNCERTAIN)

    session = body.get("session")
    if (not isinstance(session, dict) or not isinstance(session.get("name"), str)
            or not isinstance(session.get("address"), str) or not isinstance(session.get("session_id"), str)):
        fail("registration answered without a complete session record; do not register again under a new name",
             EXIT_UNCERTAIN)
    print("registered session %s as %s (id %s)" % (
        terminal_text(session.get("name", name)), terminal_text(session.get("address", "?")),
        terminal_text(session.get("session_id", "?"))))
    print_directory(body.get("directory"))
    return EXIT_OK


def print_directory(directory):
    """Print the directory, one address per line, live or not."""
    if not isinstance(directory, list) or not directory:
        return
    print("directory:")
    for entry in directory:
        if not isinstance(entry, dict):
            continue
        print("  %s%s" % (terminal_text(entry.get("address", "?")), "" if entry.get("live") else " (not live)"))


def command_poll(args):
    """Long-poll the inbox until the credential is refused or another poll displaces this one."""
    credential = read_credential(args.credential_file)
    client = build_client(args, credential)

    wait = args.wait
    if wait is None:
        wait = POLL_WAIT_SECONDS
    if wait < 0 or wait > POLL_WAIT_SECONDS:
        fail("--wait must be between 0 and %d seconds" % POLL_WAIT_SECONDS)

    client.discover()

    print("polling %s (wait %ds); one inbox serves every session of this participant" % (
        client.endpoint.describe(), wait))

    while True:
        try:
            answer = client.poll(wait)
        except Ambiguous:
            # A poll has no side effect worth reporting as uncertain: say so once and ask again.
            warn("the poll did not complete; asking again")
            time.sleep(2)
            continue
        except (ConnectFailed, PinMismatch) as exc:
            fail(exc.message, exc.code)

        if answer.status == 401:
            raise credential_refused()
        if answer.status != 200:
            code = error_code(answer)
            if 400 <= answer.status < 500:
                fail("the poll was refused with HTTP %d%s" % (
                    answer.status, " " + code if code else ""), EXIT_FAILED)
            if not 500 <= answer.status < 600:
                fail("the poll received unexpected HTTP %d; no redirect was followed" % answer.status, EXIT_FAILED)
            warn("the poll answered HTTP %d%s; asking again" % (answer.status, " " + code if code else ""))
            time.sleep(2)
            continue

        body = answer.body
        if not isinstance(body, dict):
            warn("the poll answered something that is not a JSON object; asking again")
            time.sleep(2)
            continue

        if body.get("replaced") is True:
            print("another poll for this participant replaced this one; stop it or stop this adapter")
            return EXIT_REPLACED

        offers = body.get("offers")
        if not isinstance(offers, list):
            warn("the poll answer did not contain an offers array; asking again")
            time.sleep(2)
            continue
        for offer in offers:
            handle_offer(client, offer)
    return EXIT_OK


def handle_offer(client, offer):
    """Print one offer, then acknowledge it saved:true - but never acknowledge what was not printed."""
    if not isinstance(offer, dict):
        warn("an offer was not a JSON object; not acknowledging it")
        return
    message_id = offer.get("id")
    if (not isinstance(message_id, str) or not MESSAGE_ID.match(message_id)
            or not isinstance(offer.get("from"), str) or not isinstance(offer.get("to"), str)
            or not isinstance(offer.get("body"), str)):
        warn("an offer lacked a valid id, sender, recipient or body; not acknowledging it")
        return
    try:
        print_offer(offer)
    except (OSError, ValueError) as exc:
        # Acknowledging an offer that could not be shown would tell the sender's ledger a lie.
        warn("could not print an offer, so it was NOT acknowledged: %s" % describe_os_error(exc))
        raise AdapterError("stopping instead of acknowledging an offer that could not be shown", EXIT_FAILED)

    try:
        answer = client.ack(message_id, True)
    except Ambiguous:
        warn("the ack for %s did not complete; NOT retried (the sender will see delivery_uncertain)" %
             terminal_text(message_id))
        return

    if answer.status == 401:
        raise credential_refused()
    if answer.status != 200:
        warn("the ack for %s answered HTTP %d; NOT retried" % (terminal_text(message_id), answer.status))


def print_offer(offer):
    """Print from, to and body of one offer, plus the fields a reader needs to reply."""
    print("--- message %s" % terminal_text(offer.get("id", "?")))
    print("from: %s" % terminal_text(offer.get("from", "?")))
    print("to:   %s" % terminal_text(offer.get("to", "?")))
    if offer.get("reply_to"):
        print("reply_to: %s" % terminal_text(offer.get("reply_to")))
    print("at:   %s" % terminal_text(offer.get("at", "?")))
    print("body:")
    body = offer.get("body", "")
    if not isinstance(body, str):
        body = str(body)
    for line in body.split("\n"):
        print("| " + terminal_text(line))
    sys.stdout.flush()


def command_send(args):
    """Send one message and report the gateway's result honestly. Exactly one attempt."""
    sender = args.sender
    if not SESSION_NAME.match(sender):
        fail("--from must be a session name matching ^[a-z0-9_-]{1,32}$")
    recipient = args.to
    if not ADDRESS.match(recipient):
        fail("--to must be a complete name@host or name@participant address; a bare name is refused")

    body = read_body_line()
    encoded = body.encode("utf-8")
    if not body:
        fail("the message body is empty")
    if len(encoded) > MAX_BODY_BYTES:
        fail("the message body is %d bytes; the limit is %d bytes (bytes, not characters)" % (
            len(encoded), MAX_BODY_BYTES))

    message_id = uuid.uuid4().hex
    message = {"id": message_id, "from": sender, "to": recipient, "body": body}
    if args.reply_to:
        if not MESSAGE_ID.match(args.reply_to):
            fail("--reply-to must be a message id matching ^[A-Za-z0-9-]{1,64}$")
        message["reply_to"] = args.reply_to

    credential = read_credential(args.credential_file)
    client = build_client(args, credential)
    client.discover()

    print("sending %s to %s" % (message_id, recipient))
    try:
        answer = client.send(message)
    except KeyboardInterrupt:
        print("id: %s" % message_id)
        fail(
            "UNCERTAIN: sending was interrupted, so the message may have been stored. Do not resend under a new id.",
            EXIT_UNCERTAIN,
        )
    except (ConnectFailed, PinMismatch) as exc:
        # The connection or the pin failed before any request was written: it was not sent.
        print("id: %s" % message_id)
        fail("NOT SENT: %s" % exc.message, EXIT_FAILED)
    except Ambiguous:
        print("id: %s" % message_id)
        fail(
            "UNCERTAIN: the answer did not arrive, so it may have been stored. Do not resend under a new "
            "id as if it had failed; look it up with GET /participant/v1/messages/%s" % message_id,
            EXIT_UNCERTAIN,
        )

    print("id: %s" % message_id)
    return report_send(answer, message_id)


def report_send(answer, message_id):
    """Map one send answer to a result line and an exit code."""
    if answer.status == 401:
        raise credential_refused()

    if answer.status == 200:
        body = answer.body
        result = body.get("result") if isinstance(body, dict) else None
        if isinstance(result, str) and result in DECIDED_RESULTS:
            print(RESULT_MEANINGS[result])
            if result == "unknown_recipient" and isinstance(body, dict):
                print_directory(body.get("directory"))
            detail = body.get("detail") if isinstance(body, dict) else None
            if isinstance(detail, str) and detail:
                print("detail: %s" % terminal_text(detail))
            if result in UNCERTAIN_RESULTS:
                print("do not resend under a new id; look it up with GET /participant/v1/messages/%s" % message_id)
                return EXIT_UNCERTAIN
            if result == "host_stored":
                return EXIT_OK
            # session_gone, unknown_recipient and destination_unreachable are decisions that the message
            # was NOT delivered: a definite failure of this send, not a success.
            return EXIT_FAILED

        print(
            "UNCERTAIN: the gateway answered without a result this adapter knows (got %r). Treat it as "
            "maybe stored, never as failed. Do not resend under a new id." % (terminal_text(result),)
        )
        return EXIT_UNCERTAIN

    if 400 <= answer.status < 500:
        code = error_code(answer)
        print("REFUSED: the gateway answered HTTP %d%s and did not store the message" % (
            answer.status, " " + terminal_text(code) if code else ""))
        return EXIT_FAILED

    print(
        "UNCERTAIN: the gateway answered HTTP %d, which does not say whether the message was stored. "
        "Do not resend under a new id." % answer.status
    )
    return EXIT_UNCERTAIN


def read_body_line():
    """Read exactly one line of body from stdin (the line ending is not part of it)."""
    line = sys.stdin.readline()
    if line.endswith("\n"):
        line = line[:-1]
    if line.endswith("\r"):
        line = line[:-1]
    return line


# --------------------------------------------------------------------------------------------------
# Entry point
# --------------------------------------------------------------------------------------------------


def build_parser():
    """The command line: secrets never appear here.

    The shared flags belong to each subcommand (a parent parser), so "adapter.py poll --url ..." works
    and every flag is listed by that subcommand's --help.
    """
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--url", default=os.environ.get("RATATOSKR_URL"),
                        help="participant API base URL (default: RATATOSKR_URL)")
    common.add_argument("--pin", default=os.environ.get("RATATOSKR_PIN"),
                        help="certificate SHA-256 as sha256:<64 hex> (default: RATATOSKR_PIN); HTTPS only")
    common.add_argument("--credential-file", default=os.environ.get("RATATOSKR_CREDENTIAL_FILE"),
                        help="file holding the credential (default: RATATOSKR_CREDENTIAL_FILE)")
    common.add_argument("--insecure-http", action="store_true",
                        help="allow a plain-HTTP URL; warns on every start")

    parser = argparse.ArgumentParser(
        prog="adapter.py",
        description="A reference adapter for the Ratatoskr participant API (contract %s). UNTESTED." % CONTRACT,
    )
    subparsers = parser.add_subparsers(dest="command")

    pair = subparsers.add_parser("pair", parents=[common],
                                 help="redeem a pairing code and write the credential file")
    pair.add_argument("--pair-code-file", default=None,
                      help="file holding the one-time pairing code (default: RATATOSKR_PAIR_CODE)")
    pair.add_argument("--participant", default=None, help="the participant id the code was minted for")
    pair.set_defaults(handler=command_pair)

    register = subparsers.add_parser("register", parents=[common], help="register one session name")
    register.add_argument("name")
    register.set_defaults(handler=command_register)

    poll = subparsers.add_parser("poll", parents=[common],
                                 help="long-poll the inbox, print offers and acknowledge them")
    poll.add_argument("--wait", type=int, default=None,
                      help="seconds to wait, 0..%d (default %d)" % (POLL_WAIT_SECONDS, POLL_WAIT_SECONDS))
    poll.set_defaults(handler=command_poll)

    send = subparsers.add_parser("send", parents=[common],
                                 help="send one message; the body is one line on stdin")
    send.add_argument("--from", dest="sender", required=True, help="your open session name")
    send.add_argument("--to", dest="to", required=True, help="name@host or name@participant")
    send.add_argument("--reply-to", dest="reply_to", default=None, help="the message id you are answering")
    send.set_defaults(handler=command_send)

    return parser


def main(argv=None):
    """Run one subcommand and return its exit code."""
    parser = build_parser()
    args = parser.parse_args(argv)

    if not getattr(args, "command", None):
        parser.print_help(sys.stderr)
        return EXIT_USAGE

    try:
        if not args.credential_file:
            fail("--credential-file is missing (or set RATATOSKR_CREDENTIAL_FILE)")
        return args.handler(args)
    except AdapterError as exc:
        warn("adapter: %s" % exc.message)
        return exc.code
    except Ambiguous:
        # Defensive: every command handles this itself, but a traceback would tell a reader nothing.
        warn("adapter: the request did not complete; the outcome is unknown, never a failure")
        return EXIT_UNCERTAIN
    except KeyboardInterrupt:
        warn("adapter: interrupted")
        return 130


if __name__ == "__main__":
    sys.exit(main())
