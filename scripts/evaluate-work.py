#!/usr/bin/env python3
"""Run two goal-level file tasks and repeats through real HTTP/OpenAI. Paid calls."""
import argparse
import hashlib
import http.cookiejar
import json
import time
import urllib.request
from pathlib import Path

GOALS = [
    'Проверь описание HTTP API на соответствие реализации. Исправь доказанные расхождения в методах и путях маршрутов в документации API. Не расширяй покрытие документации и не меняй стиль, если описанные факты уже верны.',
    'Изучи подключение MCP в проекте и создай ADR с объяснением архитектуры, ограничений и последствий решения, со ссылками на исследованные исходники. Если подходящий ADR уже существует и соответствует реализации, не меняй его.',
]


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--url', default='http://127.0.0.1:8080')
    p.add_argument('--workspace', default='.workspaces/project')
    p.add_argument('--output', default='artifacts/work34')
    p.add_argument('--seed-doc-mismatch', action='store_true')
    args = p.parse_args()
    workspace = Path(args.workspace).resolve()
    if workspace == Path(__file__).resolve().parents[1]:
        raise SystemExit('Use an isolated workspace, never the application checkout')
    out = Path(args.output)
    out.mkdir(parents=True, exist_ok=True)
    if (out / 'evaluation.json').exists():
        raise SystemExit('Report already exists; use another --output directory')
    if args.seed_doc_mismatch:
        target = workspace / 'docs/api.md'
        text = target.read_text()
        if not any(x in text for x in ['`GET /api/history`', '`POST /api/history`']) or not any(x in text for x in ['`GET /healthz`', '`GET /health`']):
            raise SystemExit('Expected baseline documentation not found')
        target.write_text(text.replace('`GET /api/history`', '`POST /api/history`').replace('`GET /healthz`', '`GET /health`'))
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def api(route, body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(args.url + '/api/work/' + route, data=data,
                                        headers={'Content-Type': 'application/json', 'X-File-Work': '1'})
        return json.load(opener.open(request, timeout=35))

    def snapshot():
        return {str(f.relative_to(workspace)): hashlib.sha256(f.read_bytes()).hexdigest()
                for f in workspace.rglob('*') if f.is_file() and '.git' not in f.relative_to(workspace).parts}

    report = {'goals': GOALS, 'seededMismatch': args.seed_doc_mismatch, 'runs': []}
    for index, goal in enumerate(GOALS):
        for repeat in [False, True]:
            before = snapshot()
            task = api('start', {'goal': goal})
            print(f'Scenario {index+1}, repeat={repeat}: {task["id"]}', flush=True)
            deadline = time.monotonic() + 330
            while task['status'] == 'running' and time.monotonic() < deadline:
                time.sleep(2)
                task = api('task?id=' + task['id'])
            checks = {'threeDistinctFiles': len(task['reads']) >= 3,
                      'noWriteBeforeApply': snapshot() == before,
                      'completed': task['status'] in {'ready', 'no_changes'},
                      'autonomousSearch': any(e['tool'] in {'list_files', 'search_files'} for e in task['events'])}
            if repeat:
                checks['idempotentRepeat'] = task['status'] == 'no_changes' and not task['patches'] and snapshot() == before
            else:
                checks['preparedChanges'] = task['status'] == 'ready' and bool(task['patches'])
                if task['status'] == 'ready':
                    task = api('apply', {'id': task['id']})
                    checks['appliedToDisk'] = task['status'] == 'applied' and all(
                        (workspace / patch['path']).read_text() == patch['after'] for patch in task['patches'])
                else:
                    checks['appliedToDisk'] = False
                if index == 0:
                    text = (workspace / 'docs/api.md').read_text()
                    checks['correctMethodAndPath'] = '`GET /api/history`' in text and '`GET /healthz`' in text and '`POST /api/history`' not in text and '`GET /health`' not in text
                else:
                    adrs = [patch for patch in task['patches'] if patch['path'].startswith('docs/') and not patch['exists']]
                    checks['newArchitectureDocument'] = bool(adrs) and all('MCP' in patch['after'] and '.go' in patch['after'] for patch in adrs)
            run = {'scenario': index+1, 'repeat': repeat, 'checks': checks, 'passed': all(checks.values()), 'task': task}
            report['runs'].append(run)
            (out / 'evaluation.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
            print(json.dumps({'scenario': index+1, 'repeat': repeat, 'status': task['status'], 'reads': len(task['reads']), 'checks': checks}, ensure_ascii=False), flush=True)
            for patch in task['patches']:
                (out / f'{index+1}-{"repeat" if repeat else "first"}-{Path(patch["path"]).name}.patch').write_text(patch['diff'])
            if not run['passed']:
                raise SystemExit('Scenario failed; real outputs preserved. Fix the issue before repeating.')
    report['passed'] = all(run['passed'] for run in report['runs'])
    (out / 'evaluation.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
    print('All two scenarios and two repeats passed.', flush=True)


if __name__ == '__main__':
    main()
