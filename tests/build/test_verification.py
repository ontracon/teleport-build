"""Exercise verification ordering, report export, and failure gates without compiling Teleport."""

import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import textwrap
import unittest


SCRIPT = Path(__file__).resolve().parents[2] / 'build/test.sh'
SUITES = ['upstream', 'zitadel-adapter', 'auth-bridge', 'account-labels', 'proxy-bridge', 'plugin']


class VerificationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = self.root / 'source'
        (self.source / 'build.assets').mkdir(parents=True)
        (self.source / 'build.assets/versions.mk').write_text('LIBBPF_VERSION = 1.5.1\n')
        self.output = self.root / 'out'
        (self.output / 'bin').mkdir(parents=True)
        self.binary = self.output / 'bin/teleport'
        self.binary.write_bytes(b'fixture executable')
        checksum = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        (self.output / 'SHA256SUMS').write_text(f'{checksum}  bin/teleport\n')
        self.results = self.output / 'test-results'
        self.results.mkdir()
        self.marker = self.results / 'PASSED'
        self.cache = self.root / 'go-cache'
        self.cache.mkdir()
        self.tools = self.root / 'tools'
        self.tools.mkdir()
        self.log = self.root / 'commands.jsonl'

        self.tool('go', """
            import os
            import sys
            assert sys.argv[1:] == ['env', 'GOCACHE'], sys.argv
            print(os.environ['GOCACHE'])
        """)
        self.tool('python3', """
            import os
            from pathlib import Path
            import sys
            assert Path(sys.argv[1]).name == 'smoke-test.py', sys.argv
            sys.exit(int(os.environ.get('FAIL_SMOKE', '0')))
        """)
        self.tool('make', """
            import json
            import os
            from pathlib import Path
            import shlex
            import subprocess
            import sys

            assert sys.argv[1:3] == ['--no-print-directory', 'test-go-unit'], sys.argv
            settings = dict(arg.split('=', 1) for arg in sys.argv[3:])
            # Upstream Make interpolates FLAGS into a shell recipe. Parse it
            # through a real shell so unquoted regex pipes fail this check.
            decoder = "import json,sys; print(json.dumps(sys.argv[1:]))"
            command = shlex.join([sys.executable, '-c', decoder]) + ' ' + settings['FLAGS']
            flags = json.loads(subprocess.check_output(['bash', '-e', '-c', command], text=True))
            report = Path(settings['TEST_LOG_DIR'])
            record = dict(suite=report.name, flags=flags, settings=settings)
            with open(os.environ['VERIFY_COMMAND_LOG'], 'a') as stream:
                stream.write(json.dumps(record) + '\\n')
            if os.environ.get('FAIL_SUITE') == report.name:
                sys.exit(19)
            report.mkdir(parents=True, exist_ok=True)
            (report / 'unit-tests.xml').write_text('<testsuites/>')
            (report / 'unit-tests.json').write_text('{}\\n')
        """)
        self.tool('pnpm', """
            import json
            import os
            from pathlib import Path
            import sys
            assert sys.argv[1:5] == ['exec', 'jest', '--runInBand', '--runTestsByPath'], sys.argv
            assert not Path(os.environ['OUT_DIR'], 'test-results/PASSED').exists()
            with open(os.environ['VERIFY_COMMAND_LOG'], 'a') as stream:
                stream.write(json.dumps(dict(suite='web-ui', files=sys.argv[5:])) + '\\n')
            sys.exit(int(os.environ.get('FAIL_UI', '0')))
        """)

    def tool(self, name, source):
        executable = self.tools / name
        executable.write_text(f'#!{sys.executable}\n' + textwrap.dedent(source))
        executable.chmod(0o755)

    def run_verification(self, **overrides):
        self.marker.write_text('stale pass from an earlier build\n')
        self.log.write_text('')
        env = dict(os.environ, PATH=str(self.tools) + os.pathsep + os.environ['PATH'],
                   SOURCE_DIR=str(self.source), OUT_DIR=str(self.output),
                   GOCACHE=str(self.cache), BUILD_JOBS='2',
                   VERIFY_COMMAND_LOG=str(self.log), **overrides)
        result = subprocess.run(['bash', str(SCRIPT)], env=env, capture_output=True, text=True)
        records = [json.loads(line) for line in self.log.read_text().splitlines()]
        return result, records

    def test_all_suites_export_reports_before_success_marker(self):
        result, records = self.run_verification()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual([record['suite'] for record in records], SUITES + ['web-ui'])
        self.assertTrue(self.marker.exists())
        for record in records[:-1]:
            for flag in ('-race', '-count=1', '-p=1', '-ldflags=-w'):
                self.assertIn(flag, record['flags'])
            self.assertEqual(record['settings']['FIDO2'], 'off')
            report = self.results / record['suite']
            self.assertTrue((report / 'unit-tests.xml').is_file())
            self.assertTrue((report / 'unit-tests.json').is_file())
        self.assertIn('-run=^TestZitadel|^TestNewUserContext', records[3]['flags'])
        self.assertEqual(records[0]['settings']['SUBJECT'], './lib/config ./lib/modules ./lib/tbot/config')
        self.assertEqual(records[1]['settings']['SUBJECT'], './lib/zitadeloidc')
        self.assertEqual(records[2]['settings']['SUBJECT'], './lib/auth')
        self.assertEqual(records[3]['settings']['SUBJECT'], './lib/web/ui')
        self.assertEqual(records[4]['settings']['SUBJECT'], './lib/web')
        self.assertEqual(records[5]['settings']['SUBJECT'], './lib/service')
        self.assertEqual(len(records[-1]['files']), 2)
        self.assertIn('verification finished (exit 0)', (self.results / 'disk-usage.log').read_text())

    def test_each_go_failure_prevents_success_and_later_suites(self):
        for index, suite in enumerate(SUITES):
            with self.subTest(suite=suite):
                result, records = self.run_verification(FAIL_SUITE=suite)
                self.assertEqual(result.returncode, 19, result.stdout + result.stderr)
                self.assertEqual([record['suite'] for record in records], SUITES[:index + 1])
                self.assertFalse(self.marker.exists())
                self.assertIn('verification finished (exit 19)', (self.results / 'disk-usage.log').read_text())

    def test_web_ui_failure_prevents_success_marker(self):
        result, records = self.run_verification(FAIL_UI='23')
        self.assertEqual(result.returncode, 23, result.stdout + result.stderr)
        self.assertEqual(records[-1]['suite'], 'web-ui')
        self.assertFalse(self.marker.exists())

    def test_smoke_failure_stops_before_compilation(self):
        result, records = self.run_verification(FAIL_SMOKE='13')
        self.assertEqual(result.returncode, 13, result.stdout + result.stderr)
        self.assertEqual(records, [])
        self.assertFalse(self.marker.exists())

    def test_corrupt_binary_stops_before_compilation(self):
        self.binary.write_bytes(b'damaged')
        result, records = self.run_verification()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(records, [])
        self.assertFalse(self.marker.exists())


if __name__ == '__main__':
    unittest.main()
