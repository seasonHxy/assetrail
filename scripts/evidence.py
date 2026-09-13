#!/usr/bin/env python3
"""Reproducible engineering evidence; no claimed production capacity or business ROI."""
import argparse
from contextlib import ExitStack
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import time
import urllib.request

from smoke import ROOT, CLI, NODE, call, cli, start


def save(path, data):
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def sources():
    files = {}
    for base, dirs, names in os.walk(ROOT):
        dirs[:] = sorted(d for d in dirs if d not in {".git", "bin", "reports", ".settlelab", "__pycache__", ".cache"})
        for name in sorted(names):
            path = Path(base) / name
            if path.suffix in {".go", ".py", ".yml"} or name in {"Makefile", "go.mod", "go.sum"}:
                files[str(path.relative_to(ROOT))] = sha(path)
    fingerprint = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()
    return {"sha256": fingerprint, "files": files}


def percentile(values, fraction):
    return sorted(values)[max(0, math.ceil(len(values) * fraction) - 1)]


def until(condition, description, seconds=45):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        result = condition()
        if result:
            return result
        time.sleep(0.1)
    raise TimeoutError(description)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def receipts(url, hashes):
    result = {}
    for offset in range(0, len(hashes), 32):
        group = hashes[offset:offset + 32]
        payload = [{"jsonrpc": "2.0", "id": i, "method": "chain_getTransactionReceipt", "params": [h]} for i, h in enumerate(group)]
        request = urllib.request.Request(url, data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(request, timeout=10) as response:
            batch = json.load(response)
        require(len(batch) == len(group), "incomplete receipt batch")
        seen = set()
        for item in batch:
            index = item["id"]
            require(isinstance(index, int) and 0 <= index < len(group) and index not in seen and "error" not in item, "invalid receipt response")
            seen.add(index)
            result[group[index]] = item["result"]
    return result


def rejection(fn):
    try:
        fn()
    except RuntimeError as error:
        payload = error.args[0]
        if isinstance(payload, dict) and "code" in payload:
            return payload
        raise
    raise RuntimeError("operation expected to be rejected was accepted")


def command(args):
    result = subprocess.run([str(CLI), *map(str, args)], text=True, capture_output=True, timeout=30)
    return {"exitCode": result.returncode, "stdout": result.stdout, "stderr": result.stderr}


def experiment(out, report, count):
    def passed(name, details):
        report["checks"].append({"name": name, "status": "passed", "details": details})

    with ExitStack() as cleanup:
        work = Path(cleanup.enter_context(tempfile.TemporaryDirectory(prefix="settlelab-evidence-")))
        processes, urls, generations = {}, {}, {}

        def kill_all():
            for proc in processes.values():
                if proc.poll() is None:
                    proc.kill()
                    proc.wait(timeout=10)

        cleanup.callback(kill_all)
        devnet = work / "devnet"
        accounts = json.loads(cli("init", "--dir", devnet))
        names = ["authority", "follower1", "follower2", "follower3"]
        ids = {name: cli("identity", "--out", devnet / f"{name}.p2p.key") for name in names}
        authority_address = None

        def launch(name):
            extra = ["--data", devnet / f"{name}-data", "--p2p-key", devnet / f"{name}.p2p.key", "--authority", ids["authority"], "--p2p-interval", "200ms"]
            if name != "authority":
                extra += ["--peers", authority_address]
            elif authority_address:
                extra += ["--p2p-listen", authority_address.rsplit("/p2p/", 1)[0]]
            generations[name] = generations.get(name, 0) + 1
            processes[name], urls[name] = start(devnet, out / f"{name}-{generations[name]}.log", extra)

        def stop(name):
            proc = processes[name]
            if proc.poll() is None:
                proc.kill()
            proc.wait(timeout=10)

        def consistent():
            statuses = {name: call(urls[name], "chain_getStatus") for name in names}
            triples = {(s["height"], s["blockHash"], s["stateRoot"]) for s in statuses.values()}
            return statuses if len(triples) == 1 else None

        def balances(expected_nonce):
            values = {}
            for name in names:
                alice = call(urls[name], "chain_getAccount", [accounts["alice"]])
                bob = call(urls[name], "chain_getAccount", [accounts["bob"]])
                require(alice["nonce"] == str(expected_nonce) and alice["balance"] == str(1000000 - expected_nonce) and bob["balance"] == str(expected_nonce), "incorrect balances or nonce")
                values[name] = {"alice": alice, "bob": bob}
            return values

        launch("authority")
        authority_address = call(urls["authority"], "chain_getPeers")["addresses"][0]
        for name in names[1:]:
            launch(name)
        until(lambda: all(call(urls[n], "chain_getPeers")["connectedCount"] != "0" for n in names), "peers did not connect")
        report["network"] = {"mode": "one-authority-three-followers", "peerIds": ids, "accounts": accounts}

        # Sign before timing; workload timing includes actual HTTP, propagation,
        # block scheduling, durable commit and observation at the submitting follower.
        raw = [cli("sign", "--key", devnet / "alice.key", "--to", accounts["bob"], "--amount", "1", "--nonce", str(i), "--chain-id", "9001") for i in range(count)]
        samples = []
        beginning = time.monotonic()
        for nonce, transaction in enumerate(raw):
            sent = time.monotonic()
            tx_hash = call(urls["follower3"], "chain_sendRawTransaction", [transaction])
            samples.append({"hash": tx_hash, "nonce": str(nonce), "sentOffsetSeconds": sent - beginning, "admissionMs": (time.monotonic() - sent) * 1000})
        pending = {s["hash"]: s for s in samples}

        def observe():
            for tx_hash, receipt in receipts(urls["follower3"], list(pending)).items():
                if receipt is not None:
                    require(receipt["transactionHash"] == tx_hash and receipt["status"] == "committed", "incorrect workload receipt")
                    sample = pending.pop(tx_hash)
                    sample["confirmationMs"] = (time.monotonic() - beginning - sample["sentOffsetSeconds"]) * 1000
                    sample["receipt"] = receipt
            return not pending

        until(observe, "workload transactions did not all commit")
        elapsed = time.monotonic() - beginning
        checkpoint = until(consistent, "load roots did not converge")
        values = balances(count)
        save(out / "transactions.json", samples)
        save(out / "load-checkpoint.json", {"nodes": checkpoint, "accounts": values})
        latency = [s["confirmationMs"] for s in samples]
        report["load"] = {"submitted": count, "committed": len(samples), "durationSeconds": elapsed, "observedConfirmedTPS": count / elapsed, "confirmationP50Ms": percentile(latency, .5), "confirmationP95Ms": percentile(latency, .95), "confirmationMaxMs": max(latency), "admissionP95Ms": percentile([s["admissionMs"] for s in samples], .95), "blockHeight": checkpoint["authority"]["height"]}
        passed("四节点负载与账本一致性", {"confirmed": count, "checkpoint": "load-checkpoint.json", "samples": "transactions.json"})

        altered = json.loads(bytes.fromhex(raw[0][2:]))
        altered["amount"] = "2"
        forged = "0x" + json.dumps(altered, separators=(",", ":")).encode().hex()
        negatives = {"signatureTampering": rejection(lambda: call(urls["follower3"], "chain_sendRawTransaction", [forged])), "replay": rejection(lambda: call(urls["follower3"], "chain_sendRawTransaction", [raw[0]]))}
        save(out / "rpc-rejections.json", negatives)
        passed("篡改签名交易与重放拒绝", negatives)

        selected = samples[count // 2]
        export = json.loads(cli("export", "--rpc", urls["follower3"], "--hash", selected["hash"], "--out", out / "bundle.json"))
        block_height = selected["receipt"]["blockHeight"]
        # Fetch the anchor from a different replica. This experiment assumes the
        # configured authority is trusted; it is not an independent BFT quorum.
        anchor = call(urls["follower2"], "chain_getBlockHeader", [block_height])["hash"]
        require(anchor == export["claimedBlockHash"], "replicas disagree on export anchor")
        save(out / "trusted-anchor.json", {"blockHash": anchor, "height": block_height, "sourcePeerId": ids["follower2"], "trustAssumption": "same configured authority, read via another replica; not a consensus certificate"})
        full_block = call(urls["follower2"], "chain_getBlock", [block_height])
        save(out / "comparison-block.json", full_block)
        bundle_size, block_size = (out / "bundle.json").stat().st_size, (out / "comparison-block.json").stat().st_size
        report["audit"] = {"bundleBytes": bundle_size, "fullBlockBytes": block_size, "blockTransactions": len(full_block["transactions"]), "sizeReductionPercent": (1 - bundle_size / block_size) * 100, "trustAnchor": anchor}

        stop("follower1")
        old_height = int(checkpoint["follower1"]["height"])
        for _ in range(5):
            tx_hash = cli("transfer", "--rpc", urls["follower2"], "--key", devnet / "alice.key", "--to", accounts["bob"], "--amount", "1")
            cli("wait", "--rpc", urls["follower2"], "--hash", tx_hash)
        target = call(urls["authority"], "chain_getStatus")
        recovery_start = time.monotonic()
        launch("follower1")
        recovered = until(consistent, "follower failed to recover missing blocks")
        report["followerRecovery"] = {"missingBlocks": int(target["height"]) - old_height, "restartToConvergenceMs": (time.monotonic() - recovery_start) * 1000, "restoredHeight": recovered["follower1"]["height"]}
        require(report["followerRecovery"]["missingBlocks"] >= 5, "catch-up experiment did not span multiple sync pages")
        balances(count + 5)
        passed("落后节点跨批次追块", report["followerRecovery"])

        stop("authority")
        tx_hash = cli("transfer", "--rpc", urls["follower3"], "--key", devnet / "alice.key", "--to", accounts["bob"], "--amount", "1")
        pause_start = time.monotonic()
        time.sleep(2.2)
        require(all(call(urls[n], "chain_getBlockNumber") == target["height"] for n in names[1:]), "follower advanced without authority")
        require(call(urls["follower3"], "chain_getTransactionReceipt", [tx_hash]) is None, "false confirmation while authority offline")
        pause_seconds = time.monotonic() - pause_start
        recovery_start = time.monotonic()
        launch("authority")
        cli("wait", "--rpc", urls["follower3"], "--hash", tx_hash, "--timeout", "35s")
        final = until(consistent, "authority restart did not converge")
        report["authorityRecovery"] = {"outageObservationSeconds": pause_seconds, "restartToConvergenceMs": (time.monotonic() - recovery_start) * 1000, "automaticFailover": False}
        save(out / "final-checkpoint.json", {"nodes": final, "accounts": balances(count + 6)})
        passed("出块节点故障边界与恢复", report["authorityRecovery"])

        # Verification now runs after every node process is gone.
        kill_all()
        started = time.monotonic()
        verification = command(["verify", "--file", out / "bundle.json", "--block-hash", anchor])
        report["audit"]["offlineCLIWallMs"] = (time.monotonic() - started) * 1000
        require(verification["exitCode"] == 0 and json.loads(verification["stdout"])["valid"], "offline verification failed")
        save(out / "offline-verification.json", verification)
        passed("全部节点停止后的离线验真", {"result": "offline-verification.json", "scope": "signed-transaction-inclusion"})
        bundle = json.loads((out / "bundle.json").read_text())
        bundle["transaction"]["amount"] = "999"
        save(out / "tampered-bundle.json", bundle)
        bad_amount = command(["verify", "--file", out / "tampered-bundle.json", "--block-hash", anchor])
        wrong_anchor = command(["verify", "--file", out / "bundle.json", "--block-hash", "0x" + "01" * 32])
        require(bad_amount["exitCode"] == 1 and wrong_anchor["exitCode"] == 1, "tampered evidence or wrong anchor accepted")
        save(out / "offline-rejections.json", {"changedAmount": bad_amount, "wrongAnchor": wrong_anchor})
        passed("篡改证据包与错误信任锚拒绝", {"result": "offline-rejections.json"})


def render(report):
    lines = ["# SettleLab 工程价值实测报告", "", f"- 运行时间：{report['startedAt']}", f"- 结果：**{report['status']}**", f"- 平台：{report['environment']['platform']} / {report['environment']['machine']}", f"- 源码指纹：`{report['sourceFingerprint']}`", "", "本报告只证明本次受控实验观察到的工程行为，不证明市场需求、生产容量或 BFT 安全性。四个进程位于同一台机器，固定一个受信任 authority。", "", "## 实测数据", "", "| 指标 | 本次观测 |", "| --- | --- |"]
    if "load" in report:
        m = report["load"]
        lines += [f"| 提交 / 本地确认 | {m['submitted']} / {m['committed']} |", f"| 观察窗口 | {m['durationSeconds']:.3f} s |", f"| 本次观察到的确认速率 | {m['observedConfirmedTPS']:.2f} tx/s |", f"| 确认观察延迟 P50 / P95 | {m['confirmationP50Ms']:.1f} / {m['confirmationP95Ms']:.1f} ms |", f"| 入池 RPC 延迟 P95 | {m['admissionP95Ms']:.1f} ms |"]
    if "audit" in report:
        m = report["audit"]
        lines += [f"| 审计证据包 / 对应完整区块 | {m['bundleBytes']} / {m['fullBlockBytes']} bytes |", f"| 对应区块交易数 | {m['blockTransactions']} |", f"| 文件体积减少 | {m['sizeReductionPercent']:.1f}% |"]
        if "offlineCLIWallMs" in m:
            lines += [f"| 离线验真命令耗时，含进程启动 | {m['offlineCLIWallMs']:.1f} ms |"]
    if "followerRecovery" in report:
        m = report["followerRecovery"]
        lines += [f"| 重启后补齐 {m['missingBlocks']} 个区块 | {m['restartToConvergenceMs']:.1f} ms |"]
    if "authorityRecovery" in report:
        m = report["authorityRecovery"]
        lines += [f"| authority 重启至全网一致 | {m['restartToConvergenceMs']:.1f} ms |", "| 自动故障转移 | 未实现；停机期间停止确认 |"]
    lines += ["", "## 验收结果", "", "| 检查 | 结果 |", "| --- | --- |"]
    lines += [f"| {c['name']} | {c['status']} |" for c in report["checks"]]
    if "error" in report:
        lines += ["", "运行失败：" + report["error"]]
    lines += ["", "## 测量口径", "", "- 签名在计时前生成；顺序提交单账户交易到 follower，已确认 nonce 连续递增，金额为 1，GasPrice=0。", "- 出块检查间隔 1s、P2P 交换间隔 200ms；链数据位于本机临时文件系统，提交仍执行 fsync。", "- 所有提交完成后以最多 32 个请求的 JSON-RPC 批次轮询回执，轮询间隔 100ms。确认延迟包含本地 HTTP、传播、出块等待、持久化和轮询观察误差。", "- tx/s 是该短时间固定负载的确认数量 / 观察时长，不是最大 TPS、持续压测结果或跨机器网络性能；没有与数据库做性能对比。", "- 文件大小比较采用同样的两空格缩进 JSON，比较的是证据包与它实际所属的区块，不是整条历史。", "- 信任锚经另一副本读取，但这些副本信任同一个 authority；不是独立共识或信任消除。离线证明仅涵盖交易授权与特定区块收录。", "", "## 证据文件", "", "- [结构化报告](report.json)、[测试日志](validation.log)、[源码指纹](source-manifest.json)", "- [逐交易时序](transactions.json)、[负载检查点](load-checkpoint.json)、[最终检查点](final-checkpoint.json)", "- [审计包](bundle.json)、[可信锚示例](trusted-anchor.json)、[完整区块对照](comparison-block.json)", "- [离线结果](offline-verification.json)、[离线拒绝](offline-rejections.json)、[RPC 拒绝](rpc-rejections.json)", "", "重跑：在项目根目录执行 `make evidence`。生成的是新的独立运行目录，不覆盖本报告。文件清单中的 SHA-256 用于核对文件是否变化，不提供第三方认证。", ""]
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--transactions", type=int, default=64)
    parser.add_argument("--out", type=Path)
    args = parser.parse_args()
    if not 1 <= args.transactions <= 64:
        parser.error("transactions must be 1..64 for this single-account nonce-window experiment")
    now = datetime.now(timezone.utc)
    out = args.out or ROOT / "reports" / "evidence" / now.strftime("%Y%m%dT%H%M%S.%fZ")
    out = out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    source = sources()
    save(out / "source-manifest.json", source)
    report = {"schemaVersion": 1, "status": "running", "startedAt": now.isoformat(), "sourceFingerprint": source["sha256"], "environment": {"platform": platform.platform(), "machine": platform.machine(), "logicalCPUs": os.cpu_count(), "go": subprocess.check_output([os.environ.get("GO", "go"), "version"], text=True).strip()}, "configuration": {"transactions": args.transactions, "blockIntervalSeconds": 1, "p2pIntervalMs": 200, "receiptPollMs": 100}, "binaries": {"settle": sha(CLI), "node": sha(NODE)}, "checks": []}
    error = None
    try:
        with (out / "validation.log").open("w") as log:
            completed = subprocess.run(["make", "check", "TEST_FLAGS=-count=1"], cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, timeout=300)
        require(completed.returncode == 0, "make check failed; see validation.log")
        report["checks"].append({"name": "go vet 与完整 race 测试", "status": "passed", "details": {"log": "validation.log"}})
        experiment(out, report, args.transactions)
        require(sources()["sha256"] == source["sha256"], "source code changed during evidence run")
        report["status"] = "passed"
    except Exception as exc:
        report["status"] = "failed"
        report["error"] = str(exc)
        error = exc
    finally:
        report["finishedAt"] = datetime.now(timezone.utc).isoformat()
        save(out / "report.json", report)
        (out / "report.md").write_text(render(report), encoding="utf-8")
        save(out / "manifest.json", {p.name: sha(p) for p in sorted(out.iterdir()) if p.is_file() and p.name != "manifest.json"})
        print(f"{report['status'].upper()}: {out / 'report.md'}", flush=True)
    if error:
        raise SystemExit(str(error))


if __name__ == "__main__":
    main()
