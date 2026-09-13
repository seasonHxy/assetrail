#!/usr/bin/env python3
"""Run actual binaries: init, submit, commit, SIGKILL, replay, and transfer again."""
import json
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
CLI = ROOT / "bin" / "settle"
NODE = ROOT / "bin" / "settlelab-node"


def cli(*args):
    return subprocess.check_output([str(CLI), *map(str, args)], text=True, timeout=30).strip()


def call(url, method, params=None):
    data = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params or []}).encode()
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=5) as response:
        result = json.load(response)
    if "error" in result:
        raise RuntimeError(result["error"])
    return result["result"]


def start(devnet, log_path, extra_args=()):
    with log_path.open("w") as log:
        proc = subprocess.Popen([str(NODE), "--genesis", str(devnet / "genesis.json"),
                                 "--data", str(devnet / "data"), "--listen", "127.0.0.1:0",
                                 "--block-time", "1s", *map(str, extra_args)], stdout=log, stderr=log)
    try:
        for _ in range(100):
            if proc.poll() is not None:
                raise RuntimeError(log_path.read_text())
            match = re.search(r"RPC (http://127\.0\.0\.1:\d+)", log_path.read_text())
            if match:
                url = match.group(1)
                if call(url, "chain_getStatus"):
                    return proc, url
            time.sleep(0.05)
        raise TimeoutError("node did not start")
    except BaseException:
        proc.kill()
        proc.wait(timeout=5)
        raise


def main():
    with tempfile.TemporaryDirectory(prefix="settlelab-smoke-") as work:
        work = Path(work)
        devnet = work / "devnet"
        accounts = json.loads(cli("init", "--dir", devnet))
        proc, url = start(devnet, work / "node.log")
        try:
            tx_hash = cli("transfer", "--rpc", url, "--key", devnet / "alice.key",
                          "--to", accounts["bob"], "--amount", "20")
            receipt = json.loads(cli("wait", "--rpc", url, "--hash", tx_hash))
            assert receipt["status"] == "committed", receipt
            assert call(url, "chain_getBalance", [accounts["bob"]]) == "20"
            before = call(url, "chain_getStatus")
            proof = call(url, "chain_getTransactionProof", [tx_hash])
            assert proof["blockHash"] == receipt["blockHash"]
            assert proof["root"] == call(url, "chain_getBlock", ["1"])["header"]["txRoot"]
            proc.kill()  # Deliberately skip graceful shutdown after durable acknowledgement.
            proc.wait(timeout=5)
            proc, url = start(devnet, work / "restarted.log")
            after = call(url, "chain_getStatus")
            assert (before["height"], before["stateRoot"]) == (after["height"], after["stateRoot"])
            assert call(url, "chain_getTransactionReceipt", [tx_hash]) == receipt
            assert call(url, "chain_getBalance", [accounts["bob"]]) == "20"
            second = cli("transfer", "--rpc", url, "--key", devnet / "alice.key",
                         "--to", accounts["bob"], "--amount", "30")
            cli("wait", "--rpc", url, "--hash", second)
            assert call(url, "chain_getBalance", [accounts["bob"]]) == "50"
            assert call(url, "chain_getNonce", [accounts["alice"]]) == "2"
            print("PASS: signed transfers, receipts, proof query, crash recovery, and post-restart nonce progression")
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=5)


if __name__ == "__main__":
    main()
