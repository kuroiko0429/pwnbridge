"""Only tests inject minimal Pwnagotchi imports; production imports the installed API."""
import importlib.util
from pathlib import Path
import sys
import types
import unittest
import threading

pwn = types.ModuleType("pwnagotchi")
plugins = types.ModuleType("pwnagotchi.plugins")
plugins.Plugin = object
pwn.plugins = plugins
sys.modules.setdefault("pwnagotchi", pwn)
sys.modules.setdefault("pwnagotchi.plugins", plugins)
spec = importlib.util.spec_from_file_location("bridge", Path(__file__).parents[1] / "pwnbridge.py")
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)

class UI:
    """SDK State: non-reentrant lock, one listener/key, callback inside lock."""
    def __init__(self):
        self.values = {"face": "native", "status": "hello", "mode": "MANU"}
        self.lock = threading.Lock()
        self.listeners = {}
    def get(self, key):
        with self.lock:
            return self.values[key]
    def set(self, key, value):
        with self.lock:
            previous = self.values[key]
            if value != previous:
                self.values[key] = value
                if key in self.listeners:
                    self.listeners[key](previous, value)
    def on_state_change(self, key, cb):
        with self.lock:
            self.listeners[key] = cb
    def update(self, *args, **kwargs): raise AssertionError("recursive UI update")

class OverlayTests(unittest.TestCase):
    def test_interleaved_native_write_has_explicit_best_effort_contract(self):
        read = threading.Event()
        resume = threading.Event()
        class PausedUI(UI):
            pause = False
            def get(self, key):
                value = super().get(key)
                if self.pause and key == "face":
                    self.pause = False
                    read.set()
                    if not resume.wait(2): raise RuntimeError("test barrier timed out")
                return value
        ui = PausedUI()
        changes = []
        callback = lambda previous, new: changes.append((previous, new))
        ui.on_state_change("face", callback)
        overlay = bridge.Overlay(lambda: 100)
        overlay.sample(ui)
        overlay.request({"op": "set_face", "face": "override", "ttl_seconds": 30})
        ui.pause = True
        errors = []
        def sample():
            try: overlay.sample(ui)
            except Exception as error: errors.append(error)
        worker = threading.Thread(target=sample, daemon=True)
        worker.start()
        try:
            self.assertTrue(read.wait(1))
            ui.set("face", "interleaved native")
        finally:
            resume.set()
            worker.join(2)
        self.assertFalse(worker.is_alive(), "SDK lock/listener deadlock")
        self.assertEqual(errors, [])
        self.assertIs(ui.listeners["face"], callback, "must not clobber another plugin listener")
        overlay.close(ui)
        # get/set are separate SDK transactions: the unobserved intervening write
        # cannot be recovered. Never advertise atomic/latest-native restoration.
        self.assertEqual(ui.get("face"), "native")
        self.assertIn(("native", "interleaved native"), changes)
        self.assertEqual(getattr(overlay, "restoration_semantics", None),
                         "best-effort-observed-native")

    def test_ttl_preserves_native_and_clear_unload(self):
        clock = [100.0]
        overlay = bridge.Overlay(lambda: clock[0])
        ui = UI()
        self.assertEqual(overlay.sample(ui)["message"], "hello")
        overlay.request({"op": "set_face", "face": "(◕‿◕)", "ttl_seconds": 2})
        overlay.request({"op": "set_message", "message": "override", "ttl_seconds": 1})
        self.assertEqual(overlay.sample(ui)["face"], "(◕‿◕)")
        self.assertEqual(ui.get("status"), "override")
        ui.set("face", "new native")
        overlay.sample(ui)
        self.assertEqual(ui.get("face"), "(◕‿◕)")
        clock[0] = 101.1
        overlay.sample(ui)
        self.assertEqual(ui.get("status"), "hello")
        self.assertEqual(ui.get("face"), "(◕‿◕)")
        clock[0] = 102.0
        overlay.sample(ui)
        self.assertEqual(ui.get("face"), "new native")
        overlay.request({"op": "set_face", "face": "x", "ttl_seconds": 30})
        overlay.sample(ui)
        overlay.request({"op": "clear_override"})
        overlay.sample(ui)
        self.assertEqual(ui.get("face"), "new native")
        overlay.request({"op": "set_message", "message": "x", "ttl_seconds": 30})
        overlay.sample(ui)
        overlay.close(ui)
        self.assertEqual(ui.get("status"), "hello")

class ValidationTests(unittest.TestCase):
    def test_bounded_allowlist(self):
        import json
        valid = {"op": "set_message", "message": "界" * 512, "ttl_seconds": 300}
        self.assertEqual(bridge.decode_request(json.dumps(valid).encode()), valid)
        for bad in [b"{}", b"[]", b"null", b"not JSON", b"x" * 8193,
                    b'{"op":"shutdown"}', b'{"op":"clear_override","path":"/tmp/evil"}',
                    b'{"op":"clear_override","op":"clear_override"}',
                    b'{"op":"set_face","face":null,"ttl_seconds":1}',
                    b'{"op":"set_face","face":"x","ttl_seconds":true}',
                    b'{"op":"set_face","face":"x","ttl_seconds":301}',
                    b'{"op":"set_face","face":"x","ttl_seconds":0}',
                    b'{"op":"set_face","face":"\\ud800","ttl_seconds":1}',
                    b'{"op":"clear_override"} {}', b'\xff',
                    json.dumps({"op":"set_face","face":"界"*65,"ttl_seconds":1}).encode(),
                    json.dumps({"op":"set_message","message":"x"*513,"ttl_seconds":1}).encode()]:
            with self.subTest(bad=bad[:100]):
                with self.assertRaises(ValueError): bridge.decode_request(bad)
        self.assertEqual(bridge.decode_request(b'{"op":"clear_override"}'), {"op":"clear_override"})

class SocketTests(unittest.TestCase):
    def setUp(self):
        import os
        import tempfile
        from unittest.mock import patch
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        os.chmod(self.temp.name, 0o755)
        self.socket_path = str(Path(self.temp.name) / "control.sock")
        self.state_path = Path(self.temp.name) / "ui.json"
        self.patches = patch.multiple(bridge, RUNTIME_DIR=self.temp.name,
                                     CONTROL_PATH=self.socket_path,
                                     STATE_PATH=str(self.state_path), OWNER_UID=os.getuid())
        self.patches.start()
        self.addCleanup(self.patches.stop)
        self.group = patch.object(bridge.grp, "getgrnam", return_value=types.SimpleNamespace(gr_gid=os.getgid()))
        self.group.start()
        self.addCleanup(self.group.stop)
        self.plugin = bridge.Pwnbridge()
        self.plugin.options = {"socket": "/tmp/not-allowed", "path": "/tmp/not-allowed"}
        self.ui = UI()

    def rpc(self, raw):
        import socket
        import json
        with socket.socket(socket.AF_UNIX) as client:
            client.settimeout(2)
            client.connect(self.socket_path)
            client.sendall(raw + b"\n")
            response = bytearray()
            while not response.endswith(b"\n"):
                chunk = client.recv(4096)
                if not chunk: break
                response.extend(chunk)
            return json.loads(response)

    def start(self):
        self.plugin.on_loaded()
        self.addCleanup(self.plugin.on_unload, self.ui)
        self.plugin.on_ui_setup(self.ui)
        import time
        import json
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            if self.state_path.exists() and json.loads(self.state_path.read_text()).get("face") == "native": return
            time.sleep(0.01)
        self.fail("worker never observed UI")

    def test_queued_loaded_after_terminal_unload_is_noop(self):
        self.plugin.on_unload(self.ui)
        self.plugin.on_loaded()
        self.addCleanup(self.plugin.on_unload, self.ui)
        self.assertIsNone(self.plugin._thread, "queued startup ran after terminal unload")
        replacement = bridge.Pwnbridge()
        replacement.on_loaded()
        self.addCleanup(replacement.on_unload, self.ui)
        self.assertTrue(Path(self.socket_path).exists())

    def test_unload_waits_for_inflight_startup_before_replacement(self):
        from unittest.mock import patch
        entered = threading.Event()
        release = threading.Event()
        unloaded = threading.Event()
        unloading = threading.Event()
        errors = []
        publish = self.plugin._publish
        first = [True]
        def paused_publish():
            if first[0]:
                first[0] = False
                entered.set()
                if not release.wait(2): raise RuntimeError("startup barrier timed out")
            return publish()
        def load():
            try: self.plugin.on_loaded()
            except Exception as error: errors.append(error)
        def unload():
            unloading.set()
            try: self.plugin.on_unload(self.ui)
            except Exception as error: errors.append(error)
            finally: unloaded.set()
        self.addCleanup(self.plugin.on_unload, self.ui)
        with patch.object(self.plugin, "_publish", side_effect=paused_publish):
            startup = threading.Thread(target=load, daemon=True)
            startup.start()
            self.assertTrue(entered.wait(1))
            shutdown = threading.Thread(target=unload, daemon=True)
            shutdown.start()
            try:
                self.assertTrue(unloading.wait(1))
                self.assertFalse(unloaded.wait(0.05), "unload returned while startup could still bind/start")
            finally:
                release.set()
                startup.join(3)
                shutdown.join(3)
        self.assertFalse(startup.is_alive())
        self.assertFalse(shutdown.is_alive())
        self.assertEqual(errors, [])
        self.assertFalse(Path(self.socket_path).exists())
        replacement = bridge.Pwnbridge()
        replacement.on_loaded()
        self.addCleanup(replacement.on_unload, self.ui)
        self.plugin.on_loaded()  # Another delayed callback must not touch replacement.
        self.assertTrue(Path(self.socket_path).exists())
        self.assertTrue(replacement._thread.is_alive())

    def local_handle(self, raw):
        import json
        import socket
        server, client = socket.socketpair()
        with server, client:
            client.sendall(raw + b"\n")
            self.plugin._handle(server)
            return json.loads(client.recv(4096))

    def test_filesystem_failure_rolls_back_overlay_and_reports_indeterminate(self):
        from unittest.mock import patch
        self.plugin._ui = self.ui
        self.plugin._overlay.sample(self.ui)
        self.plugin._overlay.request({"op": "set_face", "face": "previous", "ttl_seconds": 100})
        self.plugin._publish()
        previous = self.plugin._overlay.overrides.copy()
        for target in ("tempfile.mkstemp", "os.fchmod", "os.replace"):
            with self.subTest(target=target):
                with patch.object(
                        bridge.tempfile if target.startswith("tempfile") else bridge.os,
                        target.split(".")[1], side_effect=OSError("injected filesystem failure")):
                    response = self.local_handle(b'{"op":"set_face","face":"new","ttl_seconds":300}')
                self.assertFalse(response["ok"])
                self.assertIn("indeterminate", response["error"])
                self.assertEqual(self.plugin._overlay.overrides, previous)
                self.assertEqual(self.ui.get("face"), "previous")
                self.assertEqual(self.ui.get("status"), "hello")
                self.plugin._publish()

    def test_partial_ui_failure_restores_previous_request_and_is_indeterminate(self):
        self.plugin._ui = self.ui
        self.plugin._publish()
        original_set = self.ui.set
        def mutate_then_fail(key, value):
            original_set(key, value)
            if value == "new": raise RuntimeError("UI failed after mutation")
        self.ui.set = mutate_then_fail
        response = self.local_handle(b'{"op":"set_face","face":"new","ttl_seconds":300}')
        self.assertFalse(response["ok"])
        self.assertIn("indeterminate", response["error"])
        self.assertEqual(self.plugin._overlay.overrides, {})
        self.assertEqual(self.ui.get("face"), "native")

    def test_socket_controls_permissions_atomic_state_and_unload(self):
        import json
        import os
        import stat
        self.start()
        info = os.stat(self.socket_path)
        self.assertTrue(stat.S_ISSOCK(info.st_mode))
        self.assertEqual(stat.S_IMODE(info.st_mode), 0o660)
        self.assertEqual(info.st_gid, os.getgid())
        self.assertEqual(stat.S_IMODE(self.state_path.stat().st_mode), 0o644)
        self.assertTrue(self.rpc(b'{"op":"set_face","face":"test face","ttl_seconds":1}')["ok"])
        self.assertEqual(self.ui.get("face"), "test face")
        self.assertEqual(json.loads(self.state_path.read_text())["face"], "test face")
        self.assertTrue(self.rpc(b'{"op":"clear_override"}')["ok"])
        self.assertEqual(self.ui.get("face"), "native")
        self.assertTrue(self.rpc(b'{"op":"set_message","message":"test","ttl_seconds":10}')["ok"])
        self.plugin.on_unload(self.ui)
        self.assertEqual(self.ui.get("status"), "hello")
        self.assertFalse(Path(self.socket_path).exists())
        self.assertFalse(self.state_path.exists())
        self.assertFalse(self.plugin._thread.is_alive())

    def test_recover_only_stale_socket_and_refuse_unsafe_paths(self):
        import os
        import socket
        import json
        # A crashed process may leave a root-owned Unix socket. Recover it,
        # but never remove another live listener, a file, or a symlink.
        with socket.socket(socket.AF_UNIX) as stale:
            stale.bind(self.socket_path)
        self.start()
        other = bridge.Pwnbridge()
        with self.assertRaises(ValueError): other.on_loaded()
        self.assertTrue(self.rpc(b'{"op":"clear_override"}')["ok"])
        self.plugin.on_unload(self.ui)
        sentinel = Path(self.temp.name) / "sentinel"
        sentinel.write_text("unchanged")
        os.symlink(sentinel, self.socket_path)
        with self.assertRaises(ValueError): bridge.Pwnbridge().on_loaded()
        self.assertEqual(sentinel.read_text(), "unchanged")
        os.unlink(self.socket_path)
        os.symlink(sentinel, self.state_path)
        with self.assertRaises(ValueError): bridge.Pwnbridge().on_loaded()
        self.assertEqual(sentinel.read_text(), "unchanged")
        self.state_path.unlink()
        os.chmod(self.temp.name, 0o777)
        with self.assertRaises(ValueError): bridge.Pwnbridge().on_loaded()
        os.chmod(self.temp.name, 0o755)

    def test_ui_unavailable_is_honest_and_worker_survives(self):
        import json
        self.plugin.on_loaded()
        self.addCleanup(self.plugin.on_unload, self.ui)
        self.assertFalse(self.rpc(b'{"op":"set_face","face":"x","ttl_seconds":1}')["ok"])
        self.assertEqual(json.loads(self.state_path.read_text())["error"], "UI not ready")
        class BrokenUI(UI):
            def get(self, key): raise RuntimeError("test failed UI sensor")
        self.plugin.on_ui_update(BrokenUI())
        import time
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            if json.loads(self.state_path.read_text()).get("error") == "UI unavailable": break
            time.sleep(0.01)
        self.assertEqual(json.loads(self.state_path.read_text())["error"], "UI unavailable")
        self.assertFalse(self.rpc(b'{"op":"clear_override"}')["ok"])
        self.plugin.on_ui_update(self.ui)
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            if json.loads(self.state_path.read_text()).get("face") == "native": break
            time.sleep(0.01)
        self.assertTrue(self.rpc(b'{"op":"clear_override"}')["ok"])

    def test_malformed_slow_and_oversized_clients_are_bounded(self):
        import socket
        import time
        self.start()
        for bad in (b"[]", b'{"op":"shutdown"}', b"x" * 8193,
                    b'{"op":"clear_override","path":"/etc/passwd"}'):
            self.assertFalse(self.rpc(bad)["ok"])
        with socket.socket(socket.AF_UNIX) as slow:
            slow.settimeout(2)
            slow.connect(self.socket_path)
            slow.sendall(b"{")
            began = time.monotonic()
            self.assertIn(b'false', slow.recv(1024))
            self.assertLess(time.monotonic() - began, 1.5)
        self.assertTrue(self.rpc(b'{"op":"clear_override"}')["ok"])
        began = time.monotonic()
        for _ in range(10000): self.plugin.on_ui_update(self.ui)
        self.assertLess(time.monotonic() - began, 1)

if __name__ == "__main__": unittest.main()
