#!/usr/bin/env python3
"""Four real processes: follower submission, sync, restart repair, authority outage."""
import json
from contextlib import ExitStack
from pathlib import Path
import tempfile
import time

from smoke import call, cli, start


def until(condition, description):
    deadline = time.monotonic() + 40
    while time.monotonic() < deadline:
        if condition():
            return
        time.sleep(0.2)
    raise TimeoutError(description)


def main():
    processes = {}

    def stop_all():
        for proc in processes.values():
            if proc.poll() is None:
                proc.kill()
                proc.wait(timeout=5)

    try:
        with ExitStack() as cleanup:
            work = Path(cleanup.enter_context(tempfile.TemporaryDirectory(prefix="settlelab-network-")))
            cleanup.callback(stop_all)
            devnet = work / "devnet"
            accounts = json.loads(cli("init", "--dir", devnet))
            names = ["authority", "follower1", "follower2", "follower3"]
            ids = {name: cli("identity", "--out", devnet / f"{name}.p2p.key") for name in names}
            urls, generations = {}, {name: 0 for name in names}
            authority_address = None

            def launch(name):
                args = ["--data", devnet / f"{name}-data", "--p2p-key", devnet / f"{name}.p2p.key",
                        "--authority", ids["authority"], "--p2p-interval", "200ms"]
                if name != "authority":
                    args += ["--peers", authority_address]
                elif authority_address:
                    args += ["--p2p-listen", authority_address.rsplit("/p2p/", 1)[0]]
                generations[name] += 1
                processes[name], urls[name] = start(devnet, work / f"{name}-{generations[name]}.log", args)

            def stop(name):
                proc = processes[name]
                if proc.poll() is None:
                    proc.kill()
                proc.wait(timeout=10)

            def all_at(height):
                statuses = [call(urls[name], "chain_getStatus") for name in names]
                return all(s["height"] == str(height) for s in statuses) and len({(s["blockHash"], s["stateRoot"]) for s in statuses}) == 1

            def transfer(name, amount):
                return cli("transfer", "--rpc", urls[name], "--key", devnet / "alice.key",
                           "--to", accounts["bob"], "--amount", str(amount))

            launch("authority")
            authority_address = call(urls["authority"], "chain_getPeers")["addresses"][0]
            for name in names[1:]:
                launch(name)
            tx1 = transfer("follower3", 20)
            cli("wait", "--rpc", urls["follower3"], "--hash", tx1)
            until(lambda: all_at(1), "first block did not converge")
            assert all(call(urls[name], "chain_getBalance", [accounts["bob"]]) == "20" for name in names)

            stop("follower1")
            tx2 = transfer("follower2", 30)
            cli("wait", "--rpc", urls["follower2"], "--hash", tx2)
            launch("follower1")
            until(lambda: all_at(2), "restarted follower did not catch up")
            assert call(urls["follower1"], "chain_getTransactionReceipt", [tx2])["status"] == "committed"

            stop("authority")
            tx3 = transfer("follower3", 5)
            time.sleep(1.5)
            assert all(call(urls[name], "chain_getBlockNumber") == "2" for name in names[1:])
            assert call(urls["follower3"], "chain_getTransactionReceipt", [tx3]) is None
            launch("authority")
            cli("wait", "--rpc", urls["follower3"], "--hash", tx3, "--timeout", "35s")
            until(lambda: all_at(3), "authority restart did not resume settlement")
            assert all(call(urls[name], "chain_getBalance", [accounts["bob"]]) == "55" for name in names)
            assert all(call(urls[name], "chain_getNonce", [accounts["alice"]]) == "3" for name in names)
            print("PASS: four libp2p nodes, follower-origin transfers, identical roots, follower catch-up, authority outage and recovery")
            # Close before TemporaryDirectory removes any data directories.
            for proc in processes.values():
                if proc.poll() is None:
                    proc.terminate()
                    proc.wait(timeout=10)
    finally:
        for proc in processes.values():
            if proc.poll() is None:
                proc.kill()
                proc.wait(timeout=5)


if __name__ == "__main__":
    main()
