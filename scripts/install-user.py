#!/usr/bin/env python3
"""Install built user files only; never start a service or contact a device."""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import secrets
import stat
import sys


def directory(path, *, create=True):
    """Walk without symlinks, holding the final safe directory open."""
    path = Path(path)
    if not path.is_absolute() or '..' in path.parts:
        raise OSError(f'expected an absolute path without ..: {path}')
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY)
    try:
        for index, part in enumerate(path.parts[1:], start=1):
            if create:
                try:
                    os.mkdir(part, 0o700, dir_fd=fd)
                except FileExistsError:
                    pass
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = child
            info = os.fstat(fd)
            # Root-owned sticky ancestors (e.g. /tmp) protect user-owned children.
            # Never stage files in such a shared directory itself.
            sticky_ancestor = (info.st_uid == 0 and info.st_mode & stat.S_ISVTX
                               and index < len(path.parts) - 1)
            if (info.st_uid not in (0, os.getuid())
                    or (info.st_mode & 0o022 and not sticky_ancestor)):
                raise OSError(f'unsafe parent directory: {path}')
        return fd
    except BaseException:
        os.close(fd)
        raise


def destination(fd, name):
    """Inspect metadata only: existing private config is never opened."""
    try:
        info = os.stat(name, dir_fd=fd, follow_symlinks=False)
    except FileNotFoundError:
        return False
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
        raise OSError(f'unsafe destination: {name}')
    return True


def atomic_write(fd, name, data, mode, *, preserve=False):
    """Stage in the same directory, then publish the complete inode."""
    temporary = f'.pwnbridge-install-{secrets.token_hex(16)}'
    out = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                  0o600, dir_fd=fd)
    try:
        with os.fdopen(out, 'wb') as stream:
            stream.write(data)
            os.fchmod(stream.fileno(), mode)
            stream.flush()
            os.fsync(stream.fileno())
        destination(fd, name)
        if preserve:
            try:
                os.link(temporary, name, src_dir_fd=fd, dst_dir_fd=fd, follow_symlinks=False)
            except FileExistsError:
                destination(fd, name)
        else:
            os.replace(temporary, name, src_dir_fd=fd, dst_dir_fd=fd)
    finally:
        try:
            os.unlink(temporary, dir_fd=fd)
        except FileNotFoundError:
            pass


def source_bytes(path, limit):
    """Read a bounded regular source, never a symlink, FIFO or writable input."""
    parent = directory(path.parent, create=False)
    try:
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        with os.fdopen(fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            if (not stat.S_ISREG(info.st_mode) or info.st_uid not in (0, os.getuid())
                    or info.st_mode & 0o022 or info.st_size > limit):
                raise OSError(f'unsafe or oversized source: {path}')
            data = stream.read(limit + 1)
            if not data or len(data) > limit:
                raise OSError(f'empty or oversized source: {path}')
            return data
    finally:
        os.close(parent)


def install(project, *, home=None, environ=None):
    if os.getuid() == 0 or os.geteuid() != os.getuid():
        raise OSError('run as a normal user, not root or setuid')
    project = Path(os.path.abspath(project))
    binary_data = source_bytes(project / 'bin/pwnbridge', 64 * 1024 * 1024)
    unit_data = source_bytes(project / 'packaging/pwnbridge.service', 64 * 1024)
    home = Path.home() if home is None else Path(home)
    environ = os.environ if environ is None else environ
    xdg = Path(environ.get('XDG_CONFIG_HOME', ''))
    config_dir = xdg if xdg.is_absolute() else home / '.config'
    paths = (home / '.local/bin/pwnbridge',
             config_dir / 'systemd/user/pwnbridge.service',
             config_dir / 'pwnbridge/config.json')
    with ExitStack() as stack:
        parents = []
        for path in paths:
            fd = directory(path.parent)
            stack.callback(os.close, fd)
            parents.append(fd)
        exists = [destination(fd, path.name) for fd, path in zip(parents, paths)]
        config_data = None
        if not exists[2]:
            data = json.loads(source_bytes(project / 'packaging/config.example.json', 64 * 1024))
            data['identity_file'] = str(home / '.ssh/id_ed25519')
            config_data = (json.dumps(data, indent=2) + '\n').encode()
        atomic_write(parents[0], paths[0].name, binary_data, 0o755)
        atomic_write(parents[1], paths[1].name, unit_data, 0o644)
        if config_data is not None:
            atomic_write(parents[2], paths[2].name, config_data, 0o600, preserve=True)


def main():
    try:
        if sys.version_info < (3, 13):
            raise OSError('Python 3.13 or newer is required')
        if len(sys.argv) != 1:
            raise OSError('usage: python3 scripts/install-user.py (no arguments)')
        install(Path(__file__).absolute().parent.parent)
    except (OSError, ValueError, TypeError) as error:
        print(f'pwnbridge: installation failed: {error}. '
              'Earlier files may already have been updated; no service was started.', file=sys.stderr)
        return 1
    print('User files installed; existing config preserved. No service was started.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
