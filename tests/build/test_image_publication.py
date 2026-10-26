"""Exercise image publication with a Docker CLI stand-in; never push real images."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'build/publish-runtime.sh'
DIGEST = 'sha256:' + 'c' * 64


class ImagePublicationTests(unittest.TestCase):
    def publish(self, **overrides):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            docker = root / 'docker'
            docker.write_text(f'#!{sys.executable}\n' + textwrap.dedent('''
                import json
                import os
                from pathlib import Path
                import sys
                args = sys.argv[1:]
                with open(os.environ['DOCKER_LOG'], 'a') as stream:
                    stream.write(json.dumps(args) + '\\n')
                if args[:2] == ['image', 'inspect']:
                    value = {
                        '{{.Id}}': os.environ.get('ACTUAL_ID', 'sha256:' + 'a' * 64),
                        '{{index .Config.Labels "org.opencontainers.image.revision"}}':
                            os.environ.get('ACTUAL_REVISION', 'b' * 40),
                        '{{index .Config.Labels "org.opencontainers.image.version"}}':
                            os.environ.get('ACTUAL_VERSION', os.environ['TELEPORT_VERSION']),
                        '{{.Os}}/{{.Architecture}}': os.environ.get('ACTUAL_PLATFORM', 'linux/amd64'),
                    }[args[3]]
                    print(value)
                elif args[0] == 'tag':
                    pass
                elif args[0] == 'push':
                    sys.exit(int(os.environ.get('FAIL_PUSH', '0')))
                elif args[:3] == ['manifest', 'inspect', '--verbose']:
                    print(json.dumps({'Descriptor': {'digest': os.environ.get(
                        'REGISTRY_DIGEST', 'sha256:' + 'c' * 64)}}))
                else:
                    sys.exit('Unexpected Docker command: ' + repr(args))
            '''))
            docker.chmod(0o755)
            output = root / 'outputs'
            log = root / 'commands'
            env = dict(os.environ,
                       PATH=str(root) + os.pathsep + os.environ['PATH'],
                       TELEPORT_VERSION='18.10.0', RELEASE_BUILD='42.1',
                       GITHUB_REPOSITORY='OnTracon/Teleport-Build', GITHUB_SHA='b' * 40,
                       RUNTIME_IMAGE_ID='sha256:' + 'a' * 64,
                       GITHUB_OUTPUT=str(output), DOCKER_LOG=str(log))
            env.update(overrides)
            result = subprocess.run(['bash', str(SCRIPT)], env=env,
                                    capture_output=True, text=True)
            commands = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
            outputs = dict(line.split('=', 1) for line in output.read_text().splitlines()) if output.exists() else {}
            return result, commands, outputs

    def test_workflow_transports_the_smoke_tested_image_before_publication(self):
        workflow = (ROOT / '.github/workflows/build-linux-amd64.yml').read_text()
        build = workflow.split('\n  build:\n', 1)[1].split('\n  tsh:\n', 1)[0]
        release = workflow.split('\n  release:\n', 1)[1]
        self.assertIn('run: make release-build', build)
        self.assertLess(build.index('make test-runtime'), build.index('docker save'))
        self.assertIn('needs: [prepare, build, tsh]', release)
        self.assertIn('packages: write', release)
        self.assertIn('sha256sum --check teleport-runtime-linux-amd64.tar.gz.sha256', release)
        self.assertLess(release.index('docker load'), release.index('bash build/publish-runtime.sh'))
        self.assertIn('RUNTIME_IMAGE_ID="$(< dist/runtime/runtime-image-id.txt)"', release)
        self.assertNotIn('docker build', release)

    def test_publishes_tested_image_with_numbered_tag_and_digest(self):
        result, commands, outputs = self.publish()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        image = 'ghcr.io/ontracon/teleport-build:18.10.0-build.42.1'
        self.assertEqual(outputs, dict(image=image, digest=DIGEST))
        self.assertIn(['tag', 'teleport-zitadel:18.10.0', image], commands)
        self.assertIn(['push', image], commands)
        self.assertFalse(any(command[0] in ('build', 'login') for command in commands))

    def test_prerelease_and_build_metadata_are_valid_oci_tags(self):
        for version in ('18.10.0-rc.1', '18.10.0+vendor-patch'):
            with self.subTest(version=version):
                result, commands, outputs = self.publish(TELEPORT_VERSION=version)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue(outputs['image'].endswith(version.replace('+', '-') + '-build.42.1'))
                self.assertIn(['push', outputs['image']], commands)

    def test_wrong_image_cannot_be_published(self):
        for settings in (dict(ACTUAL_ID='wrong'), dict(ACTUAL_REVISION='wrong'),
                         dict(ACTUAL_VERSION='wrong'), dict(ACTUAL_PLATFORM='linux/arm64')):
            with self.subTest(settings=settings):
                result, commands, outputs = self.publish(**settings)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(command[0] in ('tag', 'push') for command in commands))
                self.assertFalse(outputs)

    def test_invalid_tag_or_repository_cannot_be_published(self):
        for settings in (dict(RELEASE_BUILD='42:1'), dict(TELEPORT_VERSION='bad/tag'),
                         dict(TELEPORT_VERSION='1' * 129), dict(GITHUB_REPOSITORY='bad/repo:tag')):
            with self.subTest(settings=settings):
                result, commands, outputs = self.publish(**settings)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(commands)
                self.assertFalse(outputs)

    def test_push_or_digest_failure_does_not_report_success(self):
        for settings in (dict(FAIL_PUSH='1'), dict(REGISTRY_DIGEST='not-a-digest')):
            with self.subTest(settings=settings):
                result, commands, outputs = self.publish(**settings)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(outputs)


if __name__ == '__main__':
    unittest.main()
