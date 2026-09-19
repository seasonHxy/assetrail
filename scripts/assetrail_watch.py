#!/usr/bin/env python3
"""Independent L1-data replay. Default read-only; emits challenge calldata on fraud.
All reads pin one L1 block. Re-running rebuilds from genesis, including after reorgs.
"""
import argparse
import json
from pathlib import Path
import tempfile

from assetrail_client import call, cli, decode_bytes, number, rpc, state_tuple, words, write_json, zero_state, calldata


def replay(url, contract, work, block='finalized'):
    anchor = rpc(url, 'eth_getBlockByNumber', [block, False])
    if not anchor:
        raise RuntimeError('L1 anchor block unavailable')
    block = anchor['number']
    view = lambda sig, *args: call(url, contract, sig, *args, block=block)
    config = {'l1ChainId': str(int(rpc(url, 'eth_chainId', []), 16)), 'contract': contract.lower(),
              'accounts': ['0x' + words(view('accounts(uint256)', i))[0][-20:].hex()
                           for i in range(number(view('accountCount()')))]}
    state = zero_state(); cursor = 0; sequence = 0; history = []; pending = None
    for ident in range(1, number(view('nextBatchId()')) + 1):
        fields = words(view('batchInfo(uint256)', ident))
        pre, post = ['0x' + w.hex() for w in fields[:2]]
        start, end, seq, deadline = [int.from_bytes(w, 'big') for w in fields[2:6]]
        status = int.from_bytes(fields[7], 'big')
        if status not in (1, 2):
            continue
        root = json.loads(cli('root', '--file', write_json(work / 'previous.json', state)))['root']
        if pre != root or start != cursor or seq != sequence + 1:
            raise RuntimeError('non-contiguous canonical batch history')
        deposits = []
        for i in range(start, end):
            d = words(view('deposits(uint256)', i))
            deposits.append({'account': int.from_bytes(d[1], 'big'), 'asset': int.from_bytes(d[2], 'big'),
                             'amount': str(int.from_bytes(d[3], 'big'))})
        raw = decode_bytes(view('batchData(uint256)', ident))
        operations = json.loads(cli('decode', '--data', raw))
        batch = {'config': config, 'previous': state, 'sequence': str(seq), 'depositStart': str(start),
                 'deposits': deposits, 'operations': operations}
        error = None; result = None
        try:
            result = json.loads(cli('batch', '--file', write_json(work / 'batch.json', batch)))
            if result['postRoot'] != post:
                error = 'proposed state root differs from independently executed state'
        except Exception as exc:
            error = str(exc)
        if status == 2:
            if error:
                raise RuntimeError(f'FINALIZED INVALID BATCH {ident}: {error}')
            state = result['state']; cursor = end; sequence = seq
            history.append({'id': ident, 'postRoot': post})
        else:
            pending = {'id': ident, 'deadline': deadline, 'fraud': error is not None, 'reason': error,
                       'challengeData': calldata('challenge((uint64[8],uint64[8],uint64[8]))', state_tuple(state)) if error else None}
    root = json.loads(cli('root', '--file', write_json(work / 'current.json', state)))['root']
    if root != view('stateRoot()') or cursor != number(view('depositCursor()')) or sequence != number(view('sequence()')):
        raise RuntimeError('reconstructed state does not match settlement contract')
    # A block-number snapshot is accepted only if that block still has its initial hash.
    if rpc(url, 'eth_getBlockByNumber', [block, False])['hash'] != anchor['hash']:
        raise RuntimeError('L1 reorg during replay; retry from a stable anchor')
    return {'name': 'AssetRail', 'l1Block': block, 'l1BlockHash': anchor['hash'], 'config': config,
            'state': state, 'stateRoot': root, 'depositCursor': str(cursor), 'sequence': str(sequence),
            'history': history, 'pending': pending, 'escaped': bool(number(view('escaped()')))}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--rpc', required=True); p.add_argument('--contract', required=True)
    p.add_argument('--block', default='finalized', help='use latest only on a controlled local L1')
    p.add_argument('--out')
    args = p.parse_args()
    with tempfile.TemporaryDirectory(prefix='assetrail-watch-') as tmp:
        result = replay(args.rpc, args.contract, Path(tmp), args.block)
    if args.out:
        write_json(args.out, result)
    print(json.dumps(result, indent=2))
    if result['pending'] and result['pending']['fraud']:
        raise SystemExit(2)


if __name__ == '__main__':
    main()
