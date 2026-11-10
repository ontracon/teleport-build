"""Stamp one Teleport-visible version into the pinned upstream Go API."""

import argparse
from pathlib import Path
import re


STABLE = re.compile(r'^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$')
BUILD = re.compile(r'^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$')


def stamp(source, upstream, build):
    parsed = STABLE.fullmatch(upstream)
    if not parsed or not BUILD.fullmatch(build):
        raise ValueError('Expected a stable upstream version and numeric run.attempt')
    version = f'{upstream}-{build}'
    path = source / 'api/version.go'
    content = path.read_text()
    expected = {'Version': f'"{upstream}"', 'VersionMajor': parsed[1],
                'VersionMinor': parsed[2], 'VersionPatch': parsed[3],
                'VersionPreRelease': '""', 'VersionMetadata': '""'}
    replacements = {'Version': f'"{version}"', 'VersionPreRelease': f'"{build}"'}
    for key, value in expected.items():
        expression = rf'(?m)^const {key} = {re.escape(value)}$'
        if len(re.findall(expression, content)) != 1:
            raise ValueError(f'Unexpected upstream version constant: {key}')
        if key in replacements:
            content = re.sub(expression, f'const {key} = {replacements[key]}', content)
    path.write_text(content)
    return version


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--upstream', required=True)
    parser.add_argument('--build', required=True)
    args = parser.parse_args()
    print(stamp(args.source, args.upstream, args.build))


if __name__ == '__main__':
    main()
