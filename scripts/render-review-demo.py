#!/usr/bin/env python3
"""Build a portable, read-only viewer from real saved review artifacts. No API calls."""
import argparse
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', default='artifacts/review32')
    args = parser.parse_args()
    root = Path(args.root)
    reports = []
    for directory in ['github-first', 'github-second']:
        paths = list((root / directory).glob('*/review.json'))
        if len(paths) != 1:
            raise SystemExit(f'Expected one downloaded review.json in {directory}')
        reports.append(json.loads(paths[0].read_text()))
    runs = json.loads((root / 'github-runs.json').read_text())
    for report, run in zip(reports, runs['runs']):
        if report['input']['head'] != run['headSha'] or run['conclusion'] != 'success':
            raise SystemExit('Report SHA does not match a successful GitHub run')
    data = {'reports': reports, 'github': runs,
            'controls': json.loads((root / 'evaluation/summary.json').read_text())}
    # Escape script terminators and HTML, including any hostile code in a patch.
    payload = json.dumps(data, ensure_ascii=True).replace('<', '\\u003c').replace('>', '\\u003e').replace('&', '\\u0026')
    template = Path(__file__).with_name('review-viewer.html').read_text()
    output = root / 'web'
    output.mkdir(exist_ok=True)
    (output / 'index.html').write_text(template.replace('/*REPORT_DATA*/null', payload))
    print(f'Viewer: {output / "index.html"} (saved reports, no live API calls)')


if __name__ == '__main__':
    main()
