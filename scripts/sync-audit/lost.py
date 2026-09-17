#!/usr/bin/env python3
"""Prod-only lines that existed before a sync and are gone after it.

usage: lost.py <old-upstream> <prod-before> <new-upstream> <prod-after>
A line counts as prod-only when `git diff upstream prod` adds it. Anything listed
here was either rewritten on purpose during conflict resolution or silently
overridden by the merge: every entry needs an answer.
"""
import collections
import subprocess
import sys

PATHS = ['*.go', '*.ts', '*.tsx', '*.js', '*.yaml', '*.yml', 'Dockerfile', 'makefile']


def added(a, b):
    out = subprocess.run(['git', 'diff', '-U0', '--no-color', a, b, '--', *PATHS],
                         capture_output=True, text=True, errors='replace').stdout
    res, name = collections.defaultdict(collections.Counter), None
    for line in out.split('\n'):
        if line.startswith('+++ b/'):
            name = line[6:]
        elif name and line.startswith('+') and not line.startswith('+++'):
            text = line[1:].strip()
            if len(text) > 12 and not text.startswith(('//', '*', '/*', 'import ', '"github.com')):
                res[name][text] += 1
    return res


old_up, before_ref, new_up, after_ref = sys.argv[1:5]
before, after = added(old_up, before_ref), added(new_up, after_ref)
total = 0
for name in sorted(before):
    gone = before[name] - after.get(name, collections.Counter())
    if gone:
        total += sum(gone.values())
        print(f'## {name}  ({sum(gone.values())} of {sum(before[name].values())} prod-only lines gone)')
        for text in gone:
            print('   -', text[:170])
print(f'\n{total} prod-only lines gone in total')
