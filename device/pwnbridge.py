"""Local-only UI bridge. No hardware, attack, network, or controller API access."""
import datetime
import grp
import json
import logging
import os
import queue
import socket
import stat
import tempfile
import threading
import time

RUNTIME_DIR = "/run/pwnbridge"
CONTROL_PATH = "/run/pwnbridge/control.sock"
STATE_PATH = "/run/pwnbridge/ui.json"
OWNER_UID = 0

MAX_REQUEST = 8192


def decode_request(data):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate key")
            result[key] = value
        return result
    if len(data) > MAX_REQUEST:
        raise ValueError("request too large")
    request = json.loads(data.decode("utf-8"), object_pairs_hook=unique)
    if not isinstance(request, dict):
        raise ValueError("expected object")
    op = request.get("op")
    if op == "clear_override" and set(request) == {"op"}:
        return request
    fields = {"set_face": ("face", 64), "set_message": ("message", 512)}
    if not isinstance(op, str) or op not in fields:
        raise ValueError("unsupported operation")
    field, limit = fields[op]
    if set(request) - {"op", field, "ttl_seconds"}:
        raise ValueError("unknown field")
    value = request.setdefault(field, "")
    ttl = request.get("ttl_seconds")
    if not isinstance(value, str) or len(value) > limit:
        raise ValueError("invalid text")
    value.encode("utf-8")  # Reject unpaired surrogates.
    if type(ttl) is not int or not 1 <= ttl <= 300:
        raise ValueError("invalid TTL")
    return request

import pwnagotchi.plugins as plugins


class Overlay:
    """Best-effort restoration of observed native values on monotonic expiry.

    Supported View.get/set calls are individually locked, not an atomic pair.
    A native write between them (or one equal to our override) is unobservable
    and may be overwritten. We cannot guarantee the latest native value.
    Do not acquire SDK private locks or register a state observer: State invokes
    its single listener under a non-reentrant lock; registration replaces other
    plugins' listeners. No vendor patch, monkeypatch or recursive UI update.
    """
    restoration_semantics = "best-effort-observed-native"
    def __init__(self, clock):
        self.clock = clock
        self.lock = threading.Lock()
        self.overrides = {}
        self.native = {}
        self.applied = {}

    def request(self, request):
        with self.lock:
            if request["op"] == "clear_override":
                self.overrides.clear()
            else:
                key = "face" if request["op"] == "set_face" else "status"
                value = request["face"] if key == "face" else request["message"]
                self.overrides[key] = (value, self.clock() + request["ttl_seconds"])

    def sample(self, ui):
        with self.lock:
            for key in ("face", "status"):
                value = ui.get(key)
                if key not in self.applied or value != self.applied[key]:
                    self.native[key] = value
                override = self.overrides.get(key)
                if override and self.clock() < override[1]:
                    ui.set(key, override[0])
                    self.applied[key] = override[0]
                else:
                    self.overrides.pop(key, None)
                    if key in self.applied:
                        ui.set(key, self.native[key])
                        self.applied.pop(key, None)
            return {"face": str(ui.get("face"))[:256],
                    "message": str(ui.get("status"))[:1024],
                    "mode": str(ui.get("mode"))[:128],
                    "updated_at": datetime.datetime.now(datetime.timezone.utc).isoformat()}

    def checkpoint(self, ui):
        with self.lock:
            return (self.overrides.copy(), self.native.copy(), self.applied.copy(),
                    {key: ui.get(key) for key in ("face", "status")})

    def rollback(self, checkpoint, ui):
        with self.lock:
            overrides, native, applied, values = checkpoint
            # Restore intent first, even if a UI setter subsequently fails.
            # Original absolute monotonic deadlines are never extended.
            self.overrides = overrides
            self.native = native
            self.applied = applied
            failures = []
            for key, value in values.items():
                try:
                    ui.set(key, value)
                except Exception as error:
                    failures.append(error)
            if failures:
                raise RuntimeError("UI rollback incomplete") from failures[0]
        self.sample(ui)  # Honor any previous TTL that expired during the attempt.

    def close(self, ui):
        self.request({"op": "clear_override"})
        if ui is not None:
            self.sample(ui)


class Pwnbridge(plugins.Plugin):
    __author__ = "Pwnbridge"
    __version__ = "1.0.0"
    __license__ = "GPL3"
    __description__ = "Local telemetry and temporary face/message overrides"

    def __init__(self):
        self.options = {}
        self._events = queue.Queue(maxsize=1)
        self._stop = threading.Event()
        self._lifecycle_lock = threading.Lock()
        self._overlay = Overlay(time.monotonic)
        self._ui = None
        self._listener = None
        self._thread = None
        self._socket_identity = None
        self._state_identity = None

    @staticmethod
    def _identity(path):
        info = os.lstat(path)
        return info.st_dev, info.st_ino

    @staticmethod
    def _unlink_owned(path, identity):
        try:
            if identity is not None and Pwnbridge._identity(path) == identity:
                os.unlink(path)
        except FileNotFoundError:
            pass

    def on_loaded(self):
        # Plugin events can already be queued on another SDK worker. Unload is
        # terminal for this instance and waits for startup and worker cleanup.
        with self._lifecycle_lock:
            if self._stop.is_set() or self._thread is not None:
                return
            self._start()

    def _start(self):
        info = os.lstat(RUNTIME_DIR)
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != OWNER_UID or stat.S_IMODE(info.st_mode) != 0o755:
            raise ValueError("/run/pwnbridge must be a real root-owned 0755 directory")
        # Never follow symlinks, accept configurable paths, or remove another listener.
        if os.path.lexists(CONTROL_PATH):
            info = os.lstat(CONTROL_PATH)
            if not stat.S_ISSOCK(info.st_mode) or info.st_uid != OWNER_UID:
                raise ValueError("unsafe existing control path")
            identity = self._identity(CONTROL_PATH)
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as probe:
                probe.settimeout(0.1)
                try:
                    probe.connect(CONTROL_PATH)
                except ConnectionRefusedError:
                    self._unlink_owned(CONTROL_PATH, identity)
                except OSError as error:
                    raise ValueError("cannot verify stale control socket") from error
                else:
                    raise ValueError("control listener already running")
        if os.path.lexists(STATE_PATH):
            info = os.lstat(STATE_PATH)
            if not stat.S_ISREG(info.st_mode) or info.st_uid != OWNER_UID:
                raise ValueError("unsafe UI state file")
        group = grp.getgrnam("pi").gr_gid
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            listener.bind(CONTROL_PATH)
            self._socket_identity = self._identity(CONTROL_PATH)
            os.chown(CONTROL_PATH, -1, group)
            os.chmod(CONTROL_PATH, 0o660)
            listener.listen(4)
            listener.settimeout(0.1)
            self._listener = listener
            self._publish()
            self._thread = threading.Thread(target=self._serve, name="pwnbridge", daemon=True)
            self._thread.start()
        except Exception:
            listener.close()
            self._unlink_owned(CONTROL_PATH, self._socket_identity)
            self._unlink_owned(STATE_PATH, self._state_identity)
            raise

    def on_ui_setup(self, ui):
        self.on_ui_update(ui)

    def on_ui_update(self, ui):
        # Hooks may be queued already: no I/O or recursive ui.update here.
        if self._stop.is_set():
            return
        try:
            self._events.put_nowait(ui)
        except queue.Full:
            try:
                self._events.get_nowait()
            except queue.Empty:
                pass
            try:
                self._events.put_nowait(ui)
            except queue.Full:
                pass

    def _drain(self):
        try:
            self._ui = self._events.get_nowait()
        except queue.Empty:
            pass

    def _publish(self):
        state = {"face": "", "message": "", "mode": "",
                 "updated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                 "error": "UI not ready"}
        if self._ui is not None:
            try:
                state = self._overlay.sample(self._ui)
            except Exception:
                state["error"] = "UI unavailable"
        encoded = json.dumps(state, ensure_ascii=True, separators=(",", ":")).encode("ascii")
        if len(encoded) > 12000:
            raise ValueError("UI state too large")
        fd, temporary = tempfile.mkstemp(prefix=".ui-", dir=RUNTIME_DIR)
        try:
            with os.fdopen(fd, "wb") as stream:
                os.fchmod(stream.fileno(), 0o644)
                stream.write(encoded + b"\n")
            os.replace(temporary, STATE_PATH)
            self._state_identity = self._identity(STATE_PATH)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
        return "error" not in state

    def _apply_request(self, request):
        if self._ui is None:
            return {"ok": False, "error": "UI not ready"}
        try:
            checkpoint = self._overlay.checkpoint(self._ui)
        except Exception:
            return {"ok": False, "error": "UI unavailable"}
        try:
            self._overlay.request(request)
            if not self._publish():
                raise RuntimeError("UI application failed")
        except Exception:
            # UI and filesystem do not share a transaction. Roll back previous
            # intent/UI best-effort, but never describe a possibly applied request
            # as invalid/rejected, even if recovery appears to succeed.
            try:
                self._overlay.rollback(checkpoint, self._ui)
                self._publish()
            except Exception:
                logging.debug("[pwnbridge] rollback/publication incomplete", exc_info=True)
            return {"ok": False, "error": "UI request outcome indeterminate; restoration attempted"}
        return {"ok": True}

    def _handle(self, client):
        deadline = time.monotonic() + 0.25
        response = {"ok": False, "error": "invalid or timed out UI request"}
        try:
            data = bytearray()
            while b"\n" not in data:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise ValueError("deadline exceeded")
                client.settimeout(remaining)
                chunk = client.recv(min(4096, MAX_REQUEST + 1 - len(data)))
                if not chunk:
                    raise ValueError("incomplete request")
                data.extend(chunk)
                if len(data) > MAX_REQUEST:
                    raise ValueError("request too large")
            request = decode_request(bytes(data))
            response = self._apply_request(request)
        except (ValueError, OSError, RecursionError, TypeError):
            pass
        except Exception:
            logging.exception("[pwnbridge] UI request failed")
            response = {"ok": False, "error": "UI unavailable"}
        try:
            client.settimeout(0.1)
            client.sendall(json.dumps(response).encode("ascii") + b"\n")
        except OSError:
            pass

    def _serve(self):
        try:
            while not self._stop.is_set():
                self._drain()
                try:
                    self._publish()
                except Exception:
                    logging.exception("[pwnbridge] UI sample failed")
                try:
                    client, _ = self._listener.accept()
                except socket.timeout:
                    continue
                except OSError:
                    if self._stop.is_set():
                        break
                    raise
                with client:
                    if not self._stop.is_set():
                        self._drain()
                        self._handle(client)
        finally:
            try:
                self._overlay.close(self._ui)
            except Exception:
                logging.debug("[pwnbridge] UI unavailable during unload", exc_info=True)
            finally:
                self._unlink_owned(CONTROL_PATH, self._socket_identity)
                self._unlink_owned(STATE_PATH, self._state_identity)

    def on_unload(self, ui):
        with self._lifecycle_lock:
            self._stop.set()
            if self._listener is not None:
                self._listener.close()
            if self._thread is not None:
                self._thread.join(timeout=2)
                if self._thread.is_alive():
                    raise RuntimeError("pwnbridge worker did not stop")
