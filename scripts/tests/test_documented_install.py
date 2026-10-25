"""Execute the real documented installer only in a temporary filesystem.

No device, sudo, services, SDK or built agent is needed. Install/systemd calls
are strict allowlisted shims; root ownership is simulated, not verified.
"""
import ast
import contextlib
import copy
import datetime
import io
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tempfile
import unittest
from unittest.mock import patch

try:
    import tomlkit
except ImportError:
    tomlkit = None

REPO = Path(__file__).resolve().parents[2]
PATHS = {
    '/etc/pwnagotchi/config.toml', '/usr/local/bin/pwnbridge-agent',
    '/etc/tmpfiles.d/pwnbridge.conf', '/var/backups',
    '/usr/local/share/pwnagotchi/custom-plugins',
}
STAGED = ('pwnbridge-agent.arm64', 'pwnbridge.tmpfiles.conf', 'pwnbridge.py')


def snippet(language):
    text = (REPO / 'docs' / ('install.' + language + '.md')).read_text()
    candidates = [code for code in re.findall(r"<<'PY'\n(.*?)\nPY", text, re.S)
                  if 'stage = Path.cwd()' in code and 'backup = Path(tempfile.mkdtemp' in code]
    if len(candidates) != 1:
        raise AssertionError('Expected exactly one documented installer')
    return candidates[0]


class Redirect(ast.NodeTransformer):
    def __init__(self, root):
        self.root = root

    def visit_Constant(self, node):
        if isinstance(node.value, str) and node.value.startswith('/'):
            if node.value not in PATHS:
                raise AssertionError('Unexpected absolute installer path: ' + node.value)
            return ast.copy_location(ast.Constant(str(self.root / node.value.lstrip('/'))), node)
        return node


@unittest.skipIf(tomlkit is None, 'optional tomlkit is not installed')
class DocumentedInstallTests(unittest.TestCase):
    def exercise(self, *, text=None, layout='flat', existing=False,
                 unsafe=None, fail_install=False, corrupt=False, source=None, missing_leaf=False):
        with tempfile.TemporaryDirectory(prefix='pwnbridge-doc-test-') as directory:
            root = Path(directory)
            stage = root / 'stage'
            stage.mkdir(mode=0o700)
            for name in STAGED:
                (stage / name).write_text('sandbox staged fixture: ' + name)
            for name in ('etc/pwnagotchi', 'usr/local/bin', 'etc/tmpfiles.d', 'var/backups'):
                (root / name).mkdir(parents=True, exist_ok=True)
            plugin = root / ('custom/plugins' if existing else 'usr/local/share/pwnagotchi/custom-plugins')
            if existing or unsafe:
                (plugin.parent if missing_leaf else plugin).mkdir(parents=True, mode=0o755)
            unsafe_path = None
            if unsafe in ('symlink', 'symlink_ancestor'):
                unsafe_path = plugin if unsafe == 'symlink' else plugin.parent
                saved = root / 'symlink-target'
                unsafe_path.rename(saved)
                unsafe_path.symlink_to(saved, target_is_directory=True)
            elif unsafe in ('writable_ancestor', 'unowned_ancestor'):
                unsafe_path = plugin.parent
                if unsafe == 'writable_ancestor':
                    unsafe_path.chmod(0o777)
            if text is None:
                custom = f'custom_plugins = "{plugin}"\n' if existing or unsafe else ''
                if layout == 'flat':
                    text = '# KEEP-HEADER\nmain.name = "fixture" # KEEP-NAME\n'
                    text += ''.join('main.' + line + '\n' for line in custom.splitlines())
                    text += ('main.plugins.existing.enabled = false # KEEP-PLUGIN\n'
                             'personality.deauth = false # KEEP-RADIO\n'
                             'personality.associate = false\nui.web.enabled = false\ngrid.report = false\n')
                else:
                    text = ('# KEEP-HEADER\n[main]\nname = "fixture" # KEEP-NAME\n' + custom +
                            '[main.plugins.existing]\nenabled = false # KEEP-PLUGIN\n'
                            '[personality]\ndeauth = false # KEEP-RADIO\nassociate = false\n'
                            '[ui.web]\nenabled = false\n[grid]\nreport = false\n')
            config = root / 'etc/pwnagotchi/config.toml'
            config.write_text(text)
            config.chmod(0o600)
            expected = copy.deepcopy(tomlkit.parse(text).unwrap())
            main = expected.setdefault('main', {})
            if not main.get('custom_plugins'):
                main['custom_plugins'] = str(plugin)
            main.setdefault('plugins', {}).setdefault('pwnbridge', {})['enabled'] = True
            targets = (root / 'usr/local/bin/pwnbridge-agent',
                       root / 'etc/tmpfiles.d/pwnbridge.conf', plugin / 'pwnbridge.py')
            # Sentinels also detect premature writes when preflight must abort.
            for target in targets:
                if target.parent.exists():
                    target.write_text('original target')
            original_targets = {target: target.read_bytes() if target.exists() else None for target in targets}
            operations = []
            real_stat = Path.stat
            real_dumps = tomlkit.dumps

            def root_stat(path, *args, **kwargs):
                info = real_stat(path, *args, **kwargs)
                # Simulate trusted host ancestors, never alter their real ACLs.
                if path.is_relative_to(root) or path in root.parents:
                    values = list(info)
                    values[4] = 1000 if unsafe == 'unowned_ancestor' and path == unsafe_path else 0
                    values[5] = 0
                    if path in root.parents:
                        values[0] &= ~0o022
                    return os.stat_result(values)
                return info

            def command(args, *, check):
                self.assertIs(check, True)
                operations.append(args)
                allowed = [
                    ['install', '-o', 'root', '-g', 'root', '-m', mode,
                     str(stage / name), str(target)]
                    for name, target, mode in zip(STAGED, targets, ('0755', '0644', '0644'))
                ]
                if args in allowed:
                    if fail_install:
                        raise subprocess.CalledProcessError(1, args)
                    src, dst = Path(args[7]), Path(args[8])
                    self.assertTrue(src.resolve().is_relative_to(root))
                    self.assertTrue(dst.resolve().is_relative_to(root))
                    shutil.copyfile(src, dst)
                    dst.chmod(int(args[6], 8))
                else:
                    self.assertEqual(args, ['systemd-tmpfiles', '--create', str(targets[1])])
                    self.assertEqual(targets[1].read_bytes(), (stage / STAGED[1]).read_bytes())

            def dumps(doc):
                result = real_dumps(doc)
                if corrupt:
                    # Valid TOML, wrong meaning: exercise the semantic guard.
                    result += '\n[serialization_regression]\nunexpected = true\n'
                return result

            tree = ast.fix_missing_locations(Redirect(root).visit(ast.parse(source or snippet('ja'))))
            previous = Path.cwd()
            error = None
            try:
                os.chdir(stage)
                with patch.object(Path, 'stat', root_stat), \
                        patch.object(Path, 'lstat', lambda path: root_stat(path, follow_symlinks=False)), \
                        patch.object(subprocess, 'run', command), \
                        patch.object(tomlkit, 'dumps', dumps), contextlib.redirect_stdout(io.StringIO()):
                    try:
                        exec(compile(tree, '<documented installation sandbox>', 'exec'), {})
                    except (SystemExit, subprocess.CalledProcessError) as exc:
                        error = exc
            finally:
                os.chdir(previous)
            if unsafe or corrupt:
                self.assertIsInstance(error, SystemExit)
                self.assertEqual(config.read_text(), text)
                self.assertEqual(operations, [])
                self.assertEqual(list((root / 'var/backups').iterdir()), [])
                for target, original in original_targets.items():
                    self.assertEqual(target.read_bytes() if target.exists() else None, original)
                if missing_leaf:
                    self.assertFalse(plugin.exists())
                return
            if fail_install:
                self.assertIsInstance(error, subprocess.CalledProcessError)
                self.assertEqual(config.read_text(), text)
                self.assertFalse(any(args[0] == 'systemd-tmpfiles' for args in operations))
                return
            self.assertIsNone(error)
            self.assertEqual(tomlkit.parse(config.read_text()).unwrap(), expected)
            for comment in re.findall(r'#[^\n]*', text):
                self.assertIn(comment, config.read_text())
            self.assertEqual(stat.S_IMODE(config.stat().st_mode), 0o600)
            backups = list((root / 'var/backups').iterdir())
            self.assertEqual(len(backups), 1)
            self.assertEqual(stat.S_IMODE(backups[0].stat().st_mode), 0o700)
            self.assertEqual((backups[0] / 'config.toml').read_text(), text)
            self.assertEqual(len(operations), 4)
            for name, target in zip(STAGED, targets):
                self.assertEqual(target.read_bytes(), (stage / name).read_bytes())

    def test_bilingual_installer_ast_matches(self):
        self.assertEqual(ast.dump(ast.parse(snippet('ja'))), ast.dump(ast.parse(snippet('en'))))
        blocks = []
        for language in ('ja', 'en'):
            guide = (REPO / 'docs' / ('install.' + language + '.md')).read_text()
            blocks.append([ast.dump(ast.parse(code)) for code in
                           re.findall(r"<<'PY'\n(.*?)\nPY", guide, re.S)])
        self.assertEqual(*blocks)

    def test_flat_dotted_config(self):
        self.exercise()

    def test_ordinary_tables(self):
        self.exercise(layout='tables')

    def test_existing_custom_directory(self):
        self.exercise(existing=True)

    def test_missing_main_or_plugins(self):
        for text in ('# KEEP\npersonality.deauth = false\n',
                     '# KEEP\n[main]\nname = "fixture"\n[personality]\ndeauth = false\n'):
            with self.subTest(text=text):
                self.exercise(text=text)

    def test_existing_disabled_plugin_is_enabled(self):
        for text in ('main.plugins.pwnbridge.enabled = false # KEEP\npersonality.deauth = false\n',
                     '[main.plugins.pwnbridge]\nenabled = false # KEEP\n[personality]\ndeauth = false\n'):
            with self.subTest(text=text):
                self.exercise(text=text)

    def test_symlink_leaf_rejected(self):
        self.exercise(unsafe='symlink')

    def test_symlink_ancestor_rejected(self):
        self.exercise(unsafe='symlink_ancestor')

    def test_writable_ancestor_rejected(self):
        self.exercise(unsafe='writable_ancestor')

    def test_unowned_ancestor_rejected(self):
        self.exercise(unsafe='unowned_ancestor')

    def test_unsafe_ancestor_rejected_before_leaf_creation(self):
        for unsafe in ('symlink_ancestor', 'writable_ancestor', 'unowned_ancestor'):
            with self.subTest(unsafe=unsafe):
                self.exercise(unsafe=unsafe, missing_leaf=True)

    def test_failed_install_preserves_config(self):
        self.exercise(fail_install=True)

    def test_serialization_regression_aborts_before_installation(self):
        self.exercise(corrupt=True)

    def test_documented_xdg_fallback(self):
        import json
        for language in ('ja', 'en'):
            guide = (REPO / 'docs' / ('install.' + language + '.md')).read_text()
            blocks = re.findall(r"<<'PY'\n(.*?)\nPY", guide, re.S)
            inspection = next(code for code in blocks if 'identity_file' in code)
            permissions = [code for code in blocks if '.chmod(0o600)' in code]
            self.assertEqual(len(permissions), 1, 'Use the same safe XDG policy for chmod')
            for setting in ('', 'relative-config', 'absolute'):
                with self.subTest(language=language, xdg=setting), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    home = root / 'home'
                    absolute = root / 'absolute'
                    xdg = str(absolute) if setting == 'absolute' else setting
                    base = absolute if setting == 'absolute' else home / '.config'
                    config = base / 'pwnbridge/config.json'
                    config.parent.mkdir(parents=True)
                    config.write_text(json.dumps({'identity_file': str(root / 'fixture-key')}))
                    config.chmod(0o644)
                    with patch.dict(os.environ, {'XDG_CONFIG_HOME': xdg}), patch.object(Path, 'home', return_value=home), \
                            contextlib.redirect_stdout(io.StringIO()):
                        exec(inspection, {})
                        exec(permissions[0], {})
                    self.assertEqual(stat.S_IMODE(config.stat().st_mode), 0o600)

    def test_missing_enablement_mutation_is_detected(self):
        source = snippet('ja')
        tree = ast.parse(source)
        removed = []
        class RemoveEnablement(ast.NodeTransformer):
            def visit_Assign(self, node):
                if any(isinstance(target, ast.Subscript) and
                       isinstance(target.slice, ast.Constant) and target.slice.value == 'enabled'
                       for target in node.targets) or 'tomlkit.key' in ast.unparse(node):
                    removed.append(node)
                    return ast.copy_location(ast.Pass(), node)
                return node
        mutated = ast.unparse(RemoveEnablement().visit(tree))
        self.assertTrue(removed)
        with self.assertRaises(AssertionError):
            self.exercise(source=mutated)


if __name__ == '__main__':
    unittest.main()
