#!/usr/bin/env python3
"""Measure repeated four-process confirmation batches; preserve all samples.

Identical observer for old/new binaries: 100ms receipt polling, no long-poll
advantage counted. Signing is outside each timed trial. Not a capacity benchmark.
"""
import argparse
from contextlib import ExitStack
from datetime import datetime, timezone
import json
import platform
from pathlib import Path
import tempfile
import time

import smoke
from evidence import percentile, receipts, require, save, sha, sources, until


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--node-binary", type=Path, default=smoke.NODE)
    parser.add_argument("--block-time", choices=["100ms", "1s"], default="100ms")
    parser.add_argument("--out", type=Path)
    args = parser.parse_args()
    smoke.NODE = args.node_binary.resolve()
    out = args.out or smoke.ROOT / "reports" / "confirmation" / datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    out = out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    report = {"status": "running", "startedAt": datetime.now(timezone.utc).isoformat(), "platform": platform.platform(), "binarySHA256": sha(smoke.NODE), "harnessSourceFingerprint": sources()["sha256"], "configuration": {"blockTime": args.block_time, "p2pInterval": "200ms", "receiptPollMs": 100, "trials": 6, "transactionsPerTrial": 64, "pauseBeforeTrialSeconds": [0, .17, .43, .07, .31, .61]}, "trials": []}
    error = None
    try:
        with ExitStack() as cleanup:
            work = Path(cleanup.enter_context(tempfile.TemporaryDirectory(prefix="settlelab-confirmation-")))
            devnet = work / "devnet"
            accounts = json.loads(smoke.cli("init", "--dir", devnet))
            names = ["authority", "follower1", "follower2", "follower3"]
            urls, procs = {}, []

            def stop():
                for p in procs:
                    if p.poll() is None:
                        p.kill()
                    p.wait(timeout=10)

            cleanup.callback(stop)
            ids = {n: smoke.cli("identity", "--out", devnet / f"{n}.key") for n in names}
            authority = None
            for name in names:
                flags = ["--data", devnet / name, "--p2p-key", devnet / f"{name}.key", "--authority", ids["authority"], "--p2p-interval", "200ms", "--block-time", args.block_time]
                if authority:
                    flags += ["--peers", authority]
                proc, urls[name] = smoke.start(devnet, out / f"{name}.log", flags)
                procs.append(proc)
                if name == "authority":
                    authority = smoke.call(urls[name], "chain_getPeers")["addresses"][0]
            until(lambda: all(smoke.call(urls[n], "chain_getPeers")["connectedCount"] != "0" for n in names), "peers did not connect")
            # Prepare ALL trials before timing so CLI startup/signing gaps do not
            # reset the intended arrival schedule between confirmation batches.
            raws = [smoke.cli("sign", "--key", devnet / "alice.key", "--to", accounts["bob"], "--amount", "1", "--nonce", i, "--chain-id", "9001") for i in range(384)]
            for trial, pause in enumerate(report["configuration"]["pauseBeforeTrialSeconds"]):
                time.sleep(pause)
                beginning = time.monotonic()
                samples = []
                for i in range(64):
                    sent = time.monotonic()
                    h = smoke.call(urls["follower3"], "chain_sendRawTransaction", [raws[trial*64+i]])
                    samples.append({"hash": h, "nonce": trial*64+i, "sentOffsetSeconds": sent-beginning, "admissionMs": (time.monotonic()-sent)*1000})
                pending = {s["hash"]: s for s in samples}

                def observe():
                    for h, receipt in receipts(urls["follower3"], list(pending)).items():
                        if receipt is not None:
                            require(receipt["transactionHash"] == h and receipt["status"] == "committed", "invalid receipt")
                            sample = pending.pop(h)
                            sample["confirmationMs"] = (time.monotonic()-beginning-sample["sentOffsetSeconds"])*1000
                            sample["receipt"] = receipt
                    return not pending

                until(observe, "confirmation timed out")
                duration = time.monotonic()-beginning

                def consistent():
                    statuses = {n: smoke.call(urls[n], "chain_getStatus") for n in names}
                    return statuses if len({(s["height"], s["blockHash"], s["stateRoot"]) for s in statuses.values()}) == 1 else None

                checkpoint = until(consistent, "replicas diverged")
                expected = (trial+1)*64
                for n in names:
                    a = smoke.call(urls[n], "chain_getAccount", [accounts["alice"]])
                    b = smoke.call(urls[n], "chain_getAccount", [accounts["bob"]])
                    require((a["nonce"], a["balance"], b["balance"]) == (str(expected), str(1000000-expected), str(expected)), "invalid ledger")
                report["trials"].append({"index": trial, "durationSeconds": duration, "confirmationP95Ms": percentile([s["confirmationMs"] for s in samples], .95), "samples": samples, "checkpoint": checkpoint})
            latencies = [s["confirmationMs"] for t in report["trials"] for s in t["samples"]]
            report["summary"] = {"committed": len(latencies), "confirmationP50Ms": percentile(latencies, .5), "confirmationP95Ms": percentile(latencies, .95), "meanBatchDurationSeconds": sum(t["durationSeconds"] for t in report["trials"])/6}
            report["status"] = "passed"
    except Exception as exc:
        report["status"] = "failed"
        report["error"] = str(exc)
        error = exc
    finally:
        report["finishedAt"] = datetime.now(timezone.utc).isoformat()
        save(out / "report.json", report)
        save(out / "manifest.json", {p.name: sha(p) for p in out.iterdir() if p.is_file() and p.name != "manifest.json"})
        print(f"{report['status'].upper()}: {out / 'report.json'}", flush=True)
    if error:
        raise SystemExit(str(error))


if __name__ == "__main__":
    main()
