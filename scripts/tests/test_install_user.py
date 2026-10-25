import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import stat
import sys
import tempfile
import unittest
from unittest import mock

SCRIPT = Path(__file__).resolve().parents[1] / 'install-user.py'


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.home = self.base / 'home with spaces 雪'
        self.home.mkdir(mode=0o700)
        self.project = self.base / 'project'
        (self.project / 'bin').mkdir(parents=True)
        (self.project / 'packaging').mkdir()
        (self.project / 'bin/pwnbridge').write_bytes(b'binary fixture')
        (self.project / 'packaging/pwnbridge.service').write_bytes(b'unit fixture\n')
        example = Path(__file__).resolve().parents[2] / 'packaging/config.example.json'
        (self.project / 'packaging/config.example.json').write_bytes(example.read_bytes())

    def installer(self):
        self.assertTrue(SCRIPT.exists(), 'user installer is not implemented')
        spec = importlib.util.spec_from_file_location('install_user', SCRIPT)
        assert spec is not None and spec.loader is not None
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def install(self, **kwargs):
        return self.installer().install(self.project, home=self.home, environ={}, **kwargs)

    def test_new_config_is_private_and_uses_installing_home(self):
        self.install()
        path = self.home / '.config/pwnbridge/config.json'
        config = json.loads(path.read_bytes())
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(config['identity_file'], str(self.home / '.ssh/id_ed25519'))
        self.assertEqual(config['username'], 'pi')
        self.assertEqual(config['host_key_alias'], '10.12.194.1')
        self.assertEqual(config['endpoints'], [
            {'name': 'bluetooth', 'address': '10.77.0.2', 'interface': 'pwnpan0'},
            {'name': 'usb', 'address': '10.12.194.1', 'interface': ''},
        ])
        self.assertEqual((self.home / '.local/bin/pwnbridge').read_bytes(), b'binary fixture')
        self.assertEqual(stat.S_IMODE((self.home / '.local/bin/pwnbridge').stat().st_mode), 0o755)
        self.assertEqual((self.home / '.config/systemd/user/pwnbridge.service').read_bytes(), b'unit fixture\n')
    def test_existing_config_is_never_read_or_rewritten(self):
        path = self.home / '.config/pwnbridge/config.json'
        path.parent.mkdir(parents=True)
        path.write_bytes(b'not JSON\x00private bytes\xff')
        path.chmod(0o400)
        before = path.stat()
        # An invalid example must not matter when a config already exists.
        (self.project / 'packaging/config.example.json').write_bytes(b'invalid')
        self.install()
        self.assertEqual(path.read_bytes(), b'not JSON\x00private bytes\xff')
        after = path.stat()
        self.assertEqual((after.st_ino, after.st_mode, after.st_mtime_ns),
                         (before.st_ino, before.st_mode, before.st_mtime_ns))

    def test_refuses_symlink_destinations_and_unsafe_parents(self):
        for relative, kind in (
            ('.config/pwnbridge/config.json', 'symlink'),
            ('.local/bin/pwnbridge', 'symlink'),
            ('.config/systemd/user/pwnbridge.service', 'symlink'),
            ('.config', 'symlink'),
            ('.local/bin', 'writable'),
        ):
            with self.subTest(relative=relative, kind=kind), tempfile.TemporaryDirectory(dir=self.base) as temp:
                home = Path(temp)
                target = home / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                outside = home / 'outside'
                outside.mkdir()
                if kind == 'symlink':
                    target.symlink_to(outside / 'untouched' if target.suffix else outside)
                else:
                    target.mkdir(mode=0o777)
                    target.chmod(0o777)
                with self.assertRaises(OSError):
                    self.installer().install(self.project, home=home, environ={})
                self.assertFalse((outside / 'untouched').exists())
    def test_replaces_a_real_running_executable_without_interrupting_it(self):
        binary = self.home / '.local/bin/pwnbridge'
        binary.parent.mkdir(parents=True)
        helper = shutil.which('sleep')
        self.assertIsNotNone(helper)
        shutil.copyfile(str(helper), binary)
        binary.chmod(0o755)
        child = subprocess.Popen([str(binary), '30'])
        try:
            old_inode = binary.stat().st_ino
            self.install()
            self.assertIsNone(child.poll())
            self.assertNotEqual(binary.stat().st_ino, old_inode)
            self.assertEqual(binary.read_bytes(), b'binary fixture')
        finally:
            child.terminate()
            child.wait(timeout=5)
    def test_absolute_xdg_is_used_and_relative_xdg_is_ignored(self):
        for value in (str(self.base / 'xdg'), 'relative/config', ''):
            with self.subTest(xdg=value), tempfile.TemporaryDirectory(dir=self.base) as temp:
                home = Path(temp)
                self.installer().install(self.project, home=home,
                                         environ={'XDG_CONFIG_HOME': value})
                config_home = Path(value) if Path(value).is_absolute() else home / '.config'
                config = json.loads((config_home / 'pwnbridge/config.json').read_bytes())
                self.assertEqual(config['identity_file'], str(home / '.ssh/id_ed25519'))
                self.assertTrue((config_home / 'systemd/user/pwnbridge.service').is_file())
                if Path(value).is_absolute():
                    self.assertFalse((home / '.config').exists())
    def test_rejects_unsafe_sources_before_replacing_any_destination(self):
        source = self.project / 'bin/pwnbridge'
        for kind in ('symlink', 'writable', 'oversize', 'fifo', 'missing', 'parent_symlink'):
            with self.subTest(kind=kind):
                source.unlink(missing_ok=True)
                if kind == 'symlink':
                    source.symlink_to(self.project / 'packaging/pwnbridge.service')
                elif kind == 'writable':
                    source.write_bytes(b'unsafe')
                    source.chmod(0o666)
                elif kind == 'oversize':
                    with source.open('wb') as stream:
                        stream.truncate(64 * 1024 * 1024 + 1)
                elif kind == 'fifo':
                    os.mkfifo(source)
                elif kind == 'parent_symlink':
                    source.parent.rmdir()
                    source.parent.symlink_to(self.project / 'packaging')
                with self.assertRaises(OSError):
                    self.install()
                self.assertFalse((self.home / '.local/bin/pwnbridge').exists())
                if kind == 'parent_symlink':
                    source.parent.unlink()
                    source.parent.mkdir()

    def test_refuses_root_without_mutating_home(self):
        module = self.installer()
        with mock.patch.object(module.os, 'getuid', return_value=0):
            with self.assertRaises(OSError):
                module.install(self.project, home=self.home, environ={})
        self.assertEqual(list(self.home.iterdir()), [])
    def test_cli_installs_from_its_project_and_reports_failure(self):
        script = self.project / 'scripts/install-user.py'
        script.parent.mkdir()
        shutil.copyfile(SCRIPT, script)
        env = dict(os.environ, HOME=str(self.home), XDG_CONFIG_HOME='')
        result = subprocess.run([sys.executable, str(script)], env=env,
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.home / '.local/bin/pwnbridge').is_file())
        (self.project / 'bin/pwnbridge').unlink()
        result = subprocess.run([sys.executable, str(script)], env=env,
                                capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('installation failed', result.stderr)
        self.assertNotIn('Traceback', result.stderr)

    def test_partial_replace_failure_is_reported_and_cleans_staging(self):
        self.install()
        binary = self.home / '.local/bin/pwnbridge'
        unit = self.home / '.config/systemd/user/pwnbridge.service'
        config = self.home / '.config/pwnbridge/config.json'
        old_config = config.read_bytes()
        (self.project / 'bin/pwnbridge').write_bytes(b'new binary')
        (self.project / 'packaging/pwnbridge.service').write_bytes(b'new unit')
        module = self.installer()
        replace = module.os.replace
        def fail_unit(source, destination, **kwargs):
            if destination == 'pwnbridge.service':
                raise OSError('injected unit replacement failure')
            return replace(source, destination, **kwargs)
        with mock.patch.object(module.os, 'replace', side_effect=fail_unit):
            with self.assertRaisesRegex(OSError, 'injected unit'):
                module.install(self.project, home=self.home, environ={})
        self.assertEqual(binary.read_bytes(), b'new binary')
        self.assertEqual(unit.read_bytes(), b'unit fixture\n')
        self.assertEqual(config.read_bytes(), old_config)
        self.assertEqual(list(self.home.rglob('.pwnbridge-install-*')), [])

    def test_failed_stage_flush_leaves_old_binary_and_no_temporary_file(self):
        self.install()
        module = self.installer()
        with mock.patch.object(module.os, 'fsync', side_effect=OSError('injected flush failure')):
            with self.assertRaisesRegex(OSError, 'injected flush'):
                module.install(self.project, home=self.home, environ={})
        self.assertEqual((self.home / '.local/bin/pwnbridge').read_bytes(), b'binary fixture')
        self.assertEqual(list(self.home.rglob('.pwnbridge-install-*')), [])

    def test_config_created_concurrently_is_not_overwritten(self):
        module = self.installer()
        real_link = module.os.link
        def concurrent_config(source, destination, **kwargs):
            fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o400,
                         dir_fd=kwargs['dst_dir_fd'])
            with os.fdopen(fd, 'wb') as stream:
                stream.write(b'concurrent private config')
            return real_link(source, destination, **kwargs)
        with mock.patch.object(module.os, 'link', side_effect=concurrent_config):
            module.install(self.project, home=self.home, environ={})
        path = self.home / '.config/pwnbridge/config.json'
        self.assertEqual(path.read_bytes(), b'concurrent private config')
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o400)
        self.assertEqual(list(self.home.rglob('.pwnbridge-install-*')), [])
    def test_make_workflow_uses_installer_and_keeps_validation_targets(self):
        root = SCRIPT.parent.parent
        result = subprocess.run(['make', '--no-print-directory', '-n', 'install-user',
                                 'test', 'check', 'cross'], cwd=root,
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        commands = result.stdout
        self.assertIn('python3 scripts/install-user.py', commands)
        self.assertIn("python3 -m unittest discover -s scripts/tests", commands)
        self.assertIn('go test -race ./...', commands)
        self.assertIn('go vet ./...', commands)
        self.assertIn('python3 -m unittest discover -s device/tests', commands)
        self.assertIn('systemctl --user daemon-reload', commands)
        self.assertNotIn('install -Dm', commands)
        self.assertNotIn('enable', commands)
        self.assertNotIn('restart', commands)
        for target in ('pwnbridge-agent', 'pwnbridge-pan', 'pwnbridge-pan-pair'):
            self.assertIn(f'GOARCH=arm64 go build -trimpath -o bin/{target}.arm64', commands)
    def test_root_owned_sticky_temp_ancestor_is_safe_but_not_a_destination_parent(self):
        # Model /tmp without writing outside the approved scratch directory.
        module = self.installer()
        real_fstat = module.os.fstat
        ancestor_inode = self.base.stat().st_ino
        def sticky_ancestor(fd):
            info = real_fstat(fd)
            if info.st_ino == ancestor_inode:
                values = list(info)
                values[0] = stat.S_IFDIR | stat.S_ISVTX | 0o777
                values[4] = 0  # st_uid
                return os.stat_result(values)
            return info
        with mock.patch.object(module.os, 'fstat', side_effect=sticky_ancestor):
            module.install(self.project, home=self.home, environ={})
            with self.assertRaises(OSError):
                fd = module.directory(self.base)
                os.close(fd)


if __name__ == '__main__':
    unittest.main()
