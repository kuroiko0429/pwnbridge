"""TEST ONLY: real Unix socket bridge fixture using the test SDK stub."""
import json
import os
from pathlib import Path
import signal
import sys
import threading
from unittest.mock import patch
from types import SimpleNamespace

from test_pwnbridge import bridge, UI

runtime = Path(sys.argv[1])
stop = threading.Event()
signal.signal(signal.SIGTERM, lambda *_: stop.set())
with patch.multiple(bridge, RUNTIME_DIR=str(runtime),
                    CONTROL_PATH=str(runtime / "control.sock"),
                    STATE_PATH=str(runtime / "ui.json"), OWNER_UID=os.getuid()), \
     patch.object(bridge.grp, "getgrnam", return_value=SimpleNamespace(gr_gid=os.getgid())):
    plugin = bridge.Pwnbridge()
    ui = UI()
    plugin.on_loaded()
    plugin.on_ui_setup(ui)
    try:
        print(json.dumps({"ready": True}), flush=True)
        stop.wait(20)
    finally:
        plugin.on_unload(ui)
