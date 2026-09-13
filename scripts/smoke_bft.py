#!/usr/bin/env python3
"""Real four-process BFT evidence: SIGKILL, automatic rounds, certified sync, recovery."""
import datetime
import hashlib
import json
from pathlib import Path
import socket
import tempfile
import time
import traceback

from smoke import ROOT, NODE, CLI, call, cli, start


def until(fn, description, timeout=60):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if fn():
            return
        time.sleep(0.1)
    raise TimeoutError(description)


def main():
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S.%fZ')
    report_dir = ROOT / 'reports' / 'bft' / stamp
    report_dir.mkdir(parents=True)
    report = {'startedAt': stamp, 'result': 'FAIL', 'scenarios': [], 'processes': [], 'checkpoints': [],
              'binarySHA256': {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (NODE, CLI)}}
    files = sorted(p for p in ROOT.rglob('*') if p.is_file() and
                   p.suffix in ('.go', '.py', '.mod', '.sum') and
                   not any(x in p.relative_to(ROOT).parts for x in ('.git', 'reports', 'bin')))
    digest = hashlib.sha256()
    for p in files:
        digest.update(str(p.relative_to(ROOT)).encode() + b'\0' + p.read_bytes() + b'\0')
    report['sourceSHA256'] = digest.hexdigest()
    processes, urls, generations = {}, {}, {i: 0 for i in range(4)}
    config = None
    try:
        with tempfile.TemporaryDirectory(prefix='settlelab-bft-') as tmp:
            work = Path(tmp)
            devnet = work / 'devnet'
            sockets = [socket.socket() for _ in range(4)]
            try:
                for s in sockets:
                    s.bind(('127.0.0.1', 0))
                ports = ','.join(str(s.getsockname()[1]) for s in sockets)
            finally:
                for s in sockets:
                    s.close()
            accounts = json.loads(cli('bft-init', '--dir', devnet, '--ports', ports))
            config = json.loads((devnet / 'validators.json').read_text())
            (report_dir / 'validators.json').write_text(json.dumps(config, indent=2))
            (report_dir / 'genesis.json').write_text((devnet / 'genesis.json').read_text())
            ids = [v['publicKey'] for v in config['validators']]

            def alive():
                return [i for i, p in processes.items() if p.poll() is None]

            def stop(i):
                p = processes[i]
                if p.poll() is None:
                    p.kill()
                p.wait(timeout=10)

            def launch(i):
                generations[i] += 1
                log = report_dir / f'validator{i}-{generations[i]}.log'
                processes[i], urls[i] = start(devnet, log, [
                    '--data', devnet / f'data{i}', '--p2p-key', devnet / f'validator{i}.key',
                    '--validators', devnet / 'validators.json', '--consensus-timeout', '1s',
                    '--p2p-interval', '100ms'])
                report['processes'].append({'validator': i, 'generation': generations[i],
                                            'pid': processes[i].pid, 'rpc': urls[i], 'log': log.name})

            def status(i):
                return call(urls[i], 'chain_getStatus')

            def consensus(i):
                return call(urls[i], 'chain_getPeers')['consensus']

            def converged(height):
                values = [status(i) for i in alive()]
                return all(s['height'] == str(height) for s in values) and len({
                    (s['blockHash'], s['stateRoot']) for s in values}) == 1

            def transfer(i, amount=1):
                return cli('transfer', '--rpc', urls[i], '--key', devnet / 'alice.key',
                           '--to', accounts['bob'], '--amount', str(amount))

            def checkpoint(label, height, tx=None):
                until(lambda: converged(height), label)
                evidence = {'label': label, 'height': height, 'transaction': tx, 'validators': {}}
                for i in alive():
                    cert = call(urls[i], 'chain_getCommitCertificate', [str(height)])
                    votes = cert['votes']
                    assert 3 <= len(votes) <= 4
                    assert len({v['validator'] for v in votes}) == len(votes)
                    assert all(v['validator'] in ids and v['phase'] == 'precommit' and
                               v['height'] == str(height) and v['blockHash'] == status(i)['blockHash'] for v in votes)
                    assert len({v['round'] for v in votes}) == 1
                    receipt = call(urls[i], 'chain_getTransactionReceipt', [tx]) if tx else None
                    if tx:
                        assert receipt['confirmationScope'] == 'certificate-verified-local'
                    evidence['validators'][str(i)] = {'status': status(i), 'consensus': consensus(i),
                                                      'certificate': cert, 'receipt': receipt}
                report['checkpoints'].append(evidence)
                return evidence

            try:
                for i in range(4):
                    launch(i)
                assert len({processes[i].pid for i in range(4)}) == 4
                tx = transfer(3, 20)
                checkpoint('normal vote propagation', 1, tx)
                report['scenarios'].append({'name': 'four_process_commit', 'result': 'PASS'})

                # Kill the actual current proposer before injecting a new transaction.
                until(lambda: len({(consensus(i)['height'], consensus(i)['round']) for i in alive()}) == 1,
                      'validators did not share a round')
                before = consensus(0)
                proposer = ids.index(before['proposer'])
                stop(proposer)
                tx = transfer(alive()[0], 30)
                ev = checkpoint('automatic proposer failover', 2, tx)
                for v in ev['validators'].values():
                    assert int(v['certificate']['votes'][0]['round']) > int(before['round'])
                    assert ids[proposer] not in {x['validator'] for x in v['certificate']['votes']}
                report['scenarios'].append({'name': 'proposer_sigkill_automatic_round', 'result': 'PASS',
                                            'stoppedValidator': proposer, 'before': before})
                launch(proposer)
                checkpoint('proposer restart certified catch-up', 2, tx)

                # Six blocks exceed the four-decision page; the stopped node must page history.
                stop(3)
                for h in range(3, 9):
                    tx = transfer(0)
                    checkpoint(f'offline peer block {h}', h, tx)
                launch(3)
                checkpoint('multi-page certified catch-up', 8, tx)
                report['scenarios'].append({'name': 'offline_peer_six_blocks_and_paged_recovery', 'result': 'PASS'})

                stop(2)
                stop(3)
                initial = {i: consensus(i) for i in alive()}
                tx = transfer(0, 5)
                until(lambda: all(int(consensus(i)['round']) >= int(initial[i]['round']) + 2 for i in alive()),
                      'two remaining validators did not automatically advance rounds')
                observed = []
                end = time.monotonic() + 2
                while time.monotonic() < end:
                    for i in alive():
                        s = status(i)
                        assert s['height'] == '8'
                        assert call(urls[i], 'chain_getTransactionReceipt', [tx]) is None
                        observed.append({'validator': i, 'status': s, 'consensus': consensus(i)})
                    time.sleep(0.2)
                report['scenarios'].append({'name': 'two_offline_no_false_confirmation', 'result': 'PASS',
                                            'initial': initial, 'observations': observed})
                launch(2)
                checkpoint('quorum restored after round catch-up', 9, tx)
                launch(3)
                checkpoint('four validators restored', 9, tx)
                report['scenarios'].append({'name': 'quorum_recovery', 'result': 'PASS'})

                for i in range(4):
                    stop(i)
                for i in range(4):
                    launch(i)
                checkpoint('full crash restart durable certificates', 9, tx)
                assert all(call(urls[i], 'chain_getBalance', [accounts['bob']]) == '61' for i in alive())
                assert all(call(urls[i], 'chain_getNonce', [accounts['alice']]) == '9' for i in alive())
                tx = transfer(1, 2)
                checkpoint('new vote after full restart', 10, tx)
                report['scenarios'].append({'name': 'all_processes_restart_and_continue', 'result': 'PASS'})
                report['result'] = 'PASS'
            finally:
                report['lastStatuses'] = {}
                for i in alive():
                    try:
                        report['lastStatuses'][str(i)] = {'status': status(i), 'network': call(urls[i], 'chain_getPeers')}
                    except Exception as error:
                        report['lastStatuses'][str(i)] = {'error': str(error)}
                for i in list(processes):
                    stop(i)
    except BaseException:
        report['error'] = traceback.format_exc()
        raise
    finally:
        for p in processes.values():
            if p.poll() is None:
                p.kill()
                p.wait(timeout=10)
        (report_dir / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
        lines = [f"# Four-process BFT: {report['result']}", '', f"Run: {stamp}", '',
                 'Four separate OS processes; authenticated TCP/libp2p; real timers; SIGKILL faults.', '',
                 'Certificate signatures are enforced by each node; this harness also checks distinct members, phases, rounds, hashes and receipts.', '',
                 '| Scenario | Result |', '| --- | --- |']
        lines += [f"| {s['name']} | {s['result']} |" for s in report['scenarios']]
        lines += ['', f"Checkpoints: {len(report['checkpoints'])}", '',
                  'Experimental evidence only: not a proof of Byzantine safety/liveness or production performance.', '',
                  'See report.json for PIDs, binary/source fingerprints, certificates, state roots and fault observations.']
        if 'error' in report:
            lines += ['', '```', report['error'], '```']
        (report_dir / 'report.md').write_text('\n'.join(lines) + '\n')
        print(f"{report['result']}: {report_dir / 'report.md'}", flush=True)


if __name__ == '__main__':
    main()
