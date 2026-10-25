# Third-party notices

Pwnbridge's original source is distributed under GNU GPL version 3; see
[LICENSE](LICENSE). The Python bridge also carries its existing `GPL3` metadata.

## godbus/dbus v5.2.4

The Go programs use `github.com/godbus/dbus/v5` v5.2.4 under the BSD 2-Clause
license. The dependency is obtained through Go modules, not vendored here.
Retain its notice when redistributing linked binaries:

```text
Copyright (c) 2013, Georg Reinke (<guelfey at gmail dot com>), Google
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions
are met:

1. Redistributions of source code must retain the above copyright notice,
this list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright
notice, this list of conditions and the following disclaimer in the
documentation and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED
TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

## External runtime software

Pwnagotchi and its Python SDK, PiSugar's manager, OpenSSH, BlueZ,
NetworkManager, systemd, D-Bus, Go and Python are external requirements;
their source, firmware and datasets are not bundled by this repository.
Pwnbridge does not redistribute collected Wi-Fi/handshake data.
