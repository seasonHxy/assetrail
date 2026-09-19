"""Small local-L1 client. No private keys, implicit network mutations or signing."""
import json
from pathlib import Path
import subprocess
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
CLI = ROOT / 'bin' / 'assetrail'


def cli(*args):
    return subprocess.check_output([str(CLI), *map(str, args)], text=True, timeout=30).strip()


def rpc(url, method, params):
    body = json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': params}).encode()
    request = urllib.request.Request(url, body, {'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=20) as response:
        result = json.load(response)
    if 'error' in result:
        raise RuntimeError(str(result['error']))
    return result['result']


def calldata(signature, *args):
    return subprocess.check_output(['cast', 'calldata', signature, *map(str, args)], text=True, timeout=15).strip()


def call(url, target, signature, *args, block='latest'):
    return rpc(url, 'eth_call', [{'to': target, 'data': calldata(signature, *args)}, block])


def words(value):
    raw = bytes.fromhex(value[2:])
    if len(raw) % 32:
        raise ValueError('malformed ABI words')
    return [raw[i:i+32] for i in range(0, len(raw), 32)]


def number(value):
    return int.from_bytes(words(value)[0], 'big')


def decode_bytes(value):
    raw = bytes.fromhex(value[2:]); offset = int.from_bytes(raw[:32], 'big')
    size = int.from_bytes(raw[offset:offset+32], 'big')
    result = raw[offset+32:offset+32+size]
    if len(result) != size:
        raise ValueError('truncated ABI bytes')
    return '0x' + result.hex()


def send(url, sender, target=None, data='0x', value=0, gas=8_000_000):
    tx = {'from': sender, 'data': data, 'value': hex(value), 'gas': hex(gas)}
    if target:
        tx['to'] = target
    tx_hash = rpc(url, 'eth_sendTransaction', [tx])
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        receipt = rpc(url, 'eth_getTransactionReceipt', [tx_hash])
        if receipt:
            if int(receipt['status'], 16) != 1:
                raise RuntimeError(f'transaction reverted: {tx_hash}')
            return receipt
        time.sleep(.1)
    raise TimeoutError(tx_hash)


def write_json(path, value):
    path = Path(path); path.write_text(json.dumps(value, indent=2) + '\n'); return path


def zero_state():
    return {key: ['0'] * 8 for key in ('cash', 'rwa', 'nonce')}


def state_tuple(state):
    return '(' + ','.join('[' + ','.join(state[k]) + ']' for k in ('cash', 'rwa', 'nonce')) + ')'
