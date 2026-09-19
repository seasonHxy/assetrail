#!/usr/bin/env python3
"""Real local L1 + Go executor: deposits, DvP, fraud challenge, withdrawal, escape."""
import datetime
import hashlib
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import traceback

from assetrail_client import ROOT, CLI, call, calldata, cli, number, rpc, send, state_tuple, write_json, zero_state
from assetrail_watch import replay


def main():
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S.%fZ')
    report_dir = ROOT / 'reports' / 'assetrail' / stamp
    report_dir.mkdir(parents=True)
    report = {'name': 'AssetRail', 'result': 'FAIL', 'scenarios': [], 'transactions': [],
              'binarySHA256': hashlib.sha256(CLI.read_bytes()).hexdigest()}
    source = sorted([*ROOT.glob('rollup/*.go'), *ROOT.glob('cmd/assetrail/*.go'),
                     *ROOT.glob('contracts/src/*.sol'), *ROOT.glob('scripts/assetrail_*.py')])
    report['sources'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in source}
    proc = None
    try:
        with tempfile.TemporaryDirectory(prefix='assetrail-') as tmp:
            work = Path(tmp)
            with socket.socket() as sock:
                sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
            url = f'http://127.0.0.1:{port}'
            with (report_dir / 'anvil.log').open('w') as log:
                proc = subprocess.Popen(['anvil', '--host', '127.0.0.1', '--port', str(port),
                                         '--chain-id', '31337', '--silent'], stdout=log, stderr=log)
            for _ in range(100):
                if proc.poll() is not None:
                    raise RuntimeError('Anvil exited')
                try:
                    owner = rpc(url, 'eth_accounts', [])[0]; break
                except Exception:
                    time.sleep(.05)
            else:
                raise TimeoutError('Anvil startup')
            keys = [work / 'seller.key', work / 'buyer.key']
            members = [json.loads(cli('keygen', '--file', k))['address'] for k in keys]
            for member in members:
                # Explicitly local development accounts, never sent to a public RPC.
                rpc(url, 'anvil_setBalance', [member, hex(10**20)])
                rpc(url, 'anvil_impersonateAccount', [member])

            def tx(target, signature, *args, sender=owner, value=0):
                receipt = send(url, sender, target, calldata(signature, *args), value)
                report['transactions'].append({'method': signature, 'hash': receipt['transactionHash'],
                                               'gasUsed': int(receipt['gasUsed'], 16)})
                return receipt

            def deploy(name, signature, *args):
                artifact = json.loads((ROOT / 'contracts' / 'out' / f'{name}.sol' / f'{name}.json').read_text())
                encoded = subprocess.check_output(['cast', 'abi-encode', signature, *map(str, args)], text=True).strip()
                receipt = send(url, owner, data=artifact['bytecode']['object'] + encoded[2:])
                report['transactions'].append({'method': f'deploy {name}', 'hash': receipt['transactionHash']})
                return receipt['contractAddress']

            cash = deploy('MockAsset', 'f(string,string,bool)', 'Test Payment', 'tPAY', 'false')
            rwa = deploy('MockAsset', 'f(string,string,bool)', 'Test RWA', 'tRWA', 'true')
            rail = deploy('AssetRail', 'f(address,address,address[],uint256,uint256,uint256)',
                          cash, rwa, '[' + ','.join(members) + ']', '60', '300', str(10**16))
            config = {'l1ChainId': '31337', 'contract': rail, 'accounts': members}
            config_file = write_json(work / 'config.json', config)
            report['deployment'] = {'l1ChainId': '31337', 'contract': rail, 'cashToken': cash,
                                    'rwaToken': rwa, 'accounts': members, 'challengeSeconds': 60, 'exitSeconds': 300}
            for a in [rail, *members]:
                tx(rwa, 'admit(address)', a)
            tx(cash, 'mint(address,uint256)', members[1], 1000)
            tx(rwa, 'mint(address,uint256)', members[0], 100)
            tx(cash, 'approve(address,uint256)', rail, 1000, sender=members[1])
            tx(rwa, 'approve(address,uint256)', rail, 100, sender=members[0])
            tx(rail, 'deposit(uint8,uint8,uint64)', 1, 0, 1000, sender=members[1])
            tx(rail, 'deposit(uint8,uint8,uint64)', 0, 1, 100, sender=members[0])
            assert number(call(url, cash, 'balanceOf(address)', rail)) == 1000
            assert number(call(url, rwa, 'balanceOf(address)', rail)) == 100

            def signed(kind, from_, to, amount, nonce, seq, price=0, counter=0):
                op = {'kind': kind, 'from': from_, 'to': to, 'amount': str(amount), 'price': str(price),
                      'nonce': str(nonce), 'counterNonce': str(counter), 'sequence': str(seq),
                      'signature': '', 'counterSignature': ''}
                path = write_json(work / 'operation.json', op)
                op = json.loads(cli('sign', '--config', config_file, '--file', path, '--key', keys[from_]))
                if kind == 2:
                    write_json(path, op)
                    op = json.loads(cli('sign', '--config', config_file, '--file', path, '--key', keys[to], '--counter'))
                return op

            def execute(previous, seq, start, deposits, ops):
                batch = {'config': config, 'previous': previous, 'sequence': str(seq), 'depositStart': str(start),
                         'deposits': deposits, 'operations': ops}
                result = json.loads(cli('batch', '--file', write_json(work / 'batch-input.json', batch)))
                return result

            def propose(result, root=None):
                tx(rail, 'propose(bytes,uint256,bytes32)', result['data'], result['depositEnd'], root or result['postRoot'], value=10**16)
                return number(call(url, rail, 'pendingId()'))

            def finalize():
                rpc(url, 'evm_increaseTime', [61]); rpc(url, 'evm_mine', [])
                tx(rail, 'finalize()')

            initial = zero_state()
            deposits = [{'account': 1, 'asset': 0, 'amount': '1000'}, {'account': 0, 'asset': 1, 'amount': '100'}]
            result = execute(initial, 1, 0, deposits, [signed(2, 0, 1, 10, 0, 1, 250)])
            # Differential verification: Solidity independently executes the Go-signed batch.
            l1_state = call(url, rail, 'evaluate((uint64[8],uint64[8],uint64[8]),bytes,uint256,uint256,uint64)',
                            state_tuple(initial), result['data'], 0, 2, 1)
            encoded = json.loads(cli('root', '--file', write_json(work / 'post.json', result['state'])))
            assert l1_state == encoded['abi'], 'Go/Solidity execution divergence'
            propose(result)
            watched = replay(url, rail, work, 'latest')
            assert watched['pending']['fraud'] is False
            finalize()
            snapshot = replay(url, rail, work, 'latest')
            assert snapshot['stateRoot'] == result['postRoot']
            assert snapshot['state']['cash'][:2] == ['250', '750']
            assert snapshot['state']['rwa'][:2] == ['90', '10']
            report['scenarios'].append({'name': 'deposit_dvp_go_solidity_equivalence', 'result': 'PASS', 'state': snapshot})

            withdrawal = execute(result['state'], 2, 2, [], [signed(4, 1, 1, 3, 1, 2)])
            fake = '0x' + 'ff' * 32
            bad_id = propose(withdrawal, fake)
            fraud = replay(url, rail, work, 'latest')
            assert fraud['pending']['fraud'] and fraud['pending']['challengeData']
            send(url, owner, rail, fraud['pending']['challengeData'])
            assert number(call(url, rail, 'pendingId()')) == 0
            assert call(url, rail, 'stateRoot()') == result['postRoot']
            report['scenarios'].append({'name': 'independent_watcher_rejects_false_root', 'result': 'PASS', 'rejectedBatch': bad_id})

            wid = propose(withdrawal)
            try:
                call(url, rail, 'claimWithdrawal(uint256,uint256)', wid, 0)
                raise AssertionError('premature withdrawal accepted')
            except RuntimeError:
                pass
            finalize(); tx(rail, 'claimWithdrawal(uint256,uint256)', wid, 0)
            assert number(call(url, rwa, 'balanceOf(address)', members[1])) == 3
            try:
                call(url, rail, 'claimWithdrawal(uint256,uint256)', wid, 0)
                raise AssertionError('duplicate withdrawal accepted')
            except RuntimeError:
                pass
            snapshot = replay(url, rail, work, 'latest')
            assert snapshot['stateRoot'] == withdrawal['postRoot']
            report['scenarios'].append({'name': 'finalized_withdrawal_and_replay_rejection', 'result': 'PASS', 'state': snapshot})

            payments = execute(snapshot['state'], 3, 2, [], [
                signed(0, 0, 1, 5, 1, 3), signed(1, 0, 1, 2, 2, 3), signed(3, 1, 1, 40, 2, 3)])
            checkpoint = rpc(url, 'evm_snapshot', [])
            propose(payments)
            assert replay(url, rail, work, 'latest')['pending'] is not None
            assert rpc(url, 'evm_revert', [checkpoint])
            after_reorg = replay(url, rail, work, 'latest')
            assert after_reorg['pending'] is None and after_reorg['stateRoot'] == snapshot['stateRoot']
            report['scenarios'].append({'name': 'l1_reorg_discards_orphaned_pending_batch', 'result': 'PASS'})
            payment_id = propose(payments)
            assert not replay(url, rail, work, 'latest')['pending']['fraud']
            finalize(); tx(rail, 'claimWithdrawal(uint256,uint256)', payment_id, 2)
            snapshot = replay(url, rail, work, 'latest')
            assert snapshot['stateRoot'] == payments['postRoot']
            assert snapshot['state']['cash'][:2] == ['245', '715']
            assert snapshot['state']['rwa'][:2] == ['88', '9']
            assert number(call(url, cash, 'balanceOf(address)', members[1])) == 40
            report['scenarios'].append({'name': 'cash_payment_rwa_transfer_and_cash_withdrawal', 'result': 'PASS'})

            # Independent watcher restart: no node database, only L1 inputs.
            with tempfile.TemporaryDirectory(prefix='assetrail-replay-') as again:
                rebuilt = replay(url, rail, Path(again), 'latest')
            assert rebuilt['state'] == snapshot['state']
            report['scenarios'].append({'name': 'l1_data_only_recovery', 'result': 'PASS'})

            tx(cash, 'mint(address,uint256)', members[1], 50)
            tx(cash, 'approve(address,uint256)', rail, 50, sender=members[1])
            tx(rail, 'deposit(uint8,uint8,uint64)', 1, 0, 50, sender=members[1])
            rpc(url, 'evm_increaseTime', [301]); rpc(url, 'evm_mine', [])
            tx(rail, 'activateEscape()')
            tx(rail, 'escapeWithdraw((uint64[8],uint64[8],uint64[8]),uint8)', state_tuple(snapshot['state']), 1, sender=members[1])
            tx(rail, 'refundDeposit(uint256)', 2, sender=members[1])
            assert number(call(url, cash, 'balanceOf(address)', members[1])) == 805
            assert number(call(url, rwa, 'balanceOf(address)', members[1])) == 12
            report['scenarios'].append({'name': 'stalled_rollup_exit_and_unconsumed_deposit_refund', 'result': 'PASS'})
            report['result'] = 'PASS'
    except BaseException:
        report['error'] = traceback.format_exc()
        raise
    finally:
        if proc and proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill(); proc.wait(timeout=5)
        write_json(report_dir / 'report.json', report)
        lines = [f"# AssetRail local L1 verification: {report['result']}", '',
                 'Real Anvil EVM, deployed Solidity contracts, Go-signed operations and independent L1 replay.', '',
                 '| Scenario | Result |', '| --- | --- |']
        lines += [f"| {s['name']} | {s['result']} |" for s in report['scenarios']]
        lines += ['', 'Test assets only. Bounded optimistic protocol; assumes an honest timely challenger.',
                  'No mainnet deployment, real RWA backing, external security audit or throughput claim.']
        (report_dir / 'report.md').write_text('\n'.join(lines) + '\n')
        print(f"{report['result']}: {report_dir / 'report.md'}", flush=True)


if __name__ == '__main__':
    main()
