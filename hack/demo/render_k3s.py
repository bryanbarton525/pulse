#!/usr/bin/env python3
"""Render the checked-in demo into one namespace; never contact a cluster."""
import argparse
from pathlib import Path
import re

FILES = ('00-targets.yaml', '05-sink.yaml', '10-policy.yaml', '20-canaries.yaml')


def render(namespace, image):
    if not re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', namespace):
        raise ValueError('namespace must be a DNS label of at most 63 characters')
    if namespace in {'default', 'kube-system', 'kube-public', 'kube-node-lease', 'shop', 'pulse-demo', 'pulse-system'}:
        raise ValueError('choose a dedicated test namespace')
    if not re.fullmatch(r'[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}', image):
        raise ValueError('target image must be pinned by sha256 digest')
    documents = []
    for filename in FILES:
        source = (Path(__file__).parent / filename).read_text()
        for document in re.split(r'^---\s*$', source, flags=re.MULTILINE):
            if re.search(r'^kind: Namespace\s*$', document, re.MULTILINE):
                continue
            for old in ('shop', 'pulse-demo', 'pulse-system'):
                document = re.sub(r'\bnamespace: ' + old + r'(?=[,}\s])', 'namespace: ' + namespace, document)
                document = document.replace('.' + old + '.svc', '.' + namespace + '.svc')
                document = document.replace(old + '/', namespace + '/')
            document = document.replace('PULSE_DEMO_TARGET_IMAGE', image)
            document = document.replace('imagePullPolicy: Never', 'imagePullPolicy: IfNotPresent')
            documents.append(document.strip())
    return '\n---\n'.join(documents) + '\n'


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--namespace', required=True)
    parser.add_argument('--target-image', required=True)
    args = parser.parse_args()
    try:
        print(render(args.namespace, args.target_image), end='')
    except ValueError as error:
        parser.error(str(error))
