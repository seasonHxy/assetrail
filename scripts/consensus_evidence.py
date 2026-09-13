#!/usr/bin/env python3
"""Capture noncached validation and the deterministic consensus fault scenarios."""
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import subprocess

from evidence import ROOT, require, save, sha, sources


def main():
    out = ROOT / "reports" / "consensus" / datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    out.mkdir(parents=True, exist_ok=False)
    source = sources()
    save(out / "source-manifest.json", source)
    report = {"status": "running", "startedAt": datetime.now(timezone.utc).isoformat(), "sourceFingerprint": source["sha256"], "platform": platform.platform(), "go": subprocess.check_output([os.environ.get("GO", "go"), "version"], text=True).strip(), "mode": "four in-process signing engines with separate fsync stores; logical timeout and message fault injection; not a four-process P2P test"}
    failure = None
    try:
        with (out / "validation.log").open("w") as log:
            result = subprocess.run(["make", "check", "TEST_FLAGS=-count=1"], cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, timeout=300)
        require(result.returncode == 0, "make check failed; see validation.log")
        command = [os.environ.get("GO", "go"), "run"]
        if platform.system() == "Darwin":
            command += ["-ldflags=-linkmode=external"]
        command += ["./cmd/consensus-demo", "--out", str(out / "scenarios")]
        with (out / "scenarios.log").open("w") as log:
            result = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, timeout=120)
        require(result.returncode == 0, "consensus scenarios failed; see scenarios.log")
        scenarios = json.loads((out / "scenarios" / "report.json").read_text())
        require(scenarios["passed"] and len(scenarios["checks"]) == 6 and all(c["passed"] for c in scenarios["checks"]), "incomplete scenario validation")
        report["scenarios"] = scenarios
        require(sources()["sha256"] == source["sha256"], "source changed during validation")
        report["status"] = "passed"
    except Exception as exc:
        failure = exc
        report["status"] = "failed"
        report["error"] = str(exc)
    finally:
        report["finishedAt"] = datetime.now(timezone.utc).isoformat()
        save(out / "report.json", report)
        lines = ["# SettleLab 共识核心验证报告", "", f"结果：**{report['status']}**", "", f"开始：{report['startedAt']}；结束：{report['finishedAt']}。", "", f"平台：{report['platform']}；{report['go']}。", "", f"源码指纹：`{report['sourceFingerprint']}`", "", "本次运行四个签名引擎、四份独立节点状态与真实 fsync 存储。消息投递、断开和超时由进程内驱动注入，不是四进程 P2P 网络测试，没有测量网络共识延迟或 TPS。", "", "| 场景 | 结果 |", "| --- | --- |"]
        labels = {"normal-and-restart": "正常两高度确认及全员重启恢复", "one-validator-offline": "一名验证者离线：其余三名确认，恢复节点验证证书后追上", "proposer-offline": "首轮提议者离线：逻辑超时后换至第 1 轮，由其余三名确认", "two-validators-offline": "两名验证者离线：四个逻辑轮次均无区块或成功回执", "lock-restart-round-change": "阻断预提交消息、保存锁、全员重启、携带旧 POL 安全重提", "equivocating-proposer": "同一提议者签出冲突提案：分流票不足以确认，三名诚实参与者换轮恢复"}
        for check in report.get("scenarios", {}).get("checks", []):
            lines += [f"| {labels[check['name']]} | {'通过' if check['passed'] else '失败'} |"]
        lines += ["", "完整测试还覆盖签名篡改、错链/阶段/轮次、重复计票、冲突票、无效执行根、锁跨重启保留、较高轮 POL、旧轮完整证书、并发重复投递、旧签名器覆盖拒绝、日志写入失败、区块写入失败、证书损坏、缺失安全日志和目录模式隔离。", "", "- [完整测试日志](validation.log) / [演示输出](scenarios.log)", "- [结构化报告](report.json) / [原始场景与提交证书](scenarios/report.json)", "- [源码文件指纹](source-manifest.json) / [证据文件 SHA-256](manifest.json)", "", "每个场景目录保留 validators.json、network.json、consensus.json 和 chain.json。测试私钥只驻留内存并在结束后清理，未写入报告；这些目录用于检查证据，不是可重新启动的持久测试网络。", "", "算法是 Tendermint 思路下的实验子集。尚缺实际 P2P 投票传播、自动 pacemaker、分阶段超时和未来轮追赶策略；没有证明一般异步调度下的活性，不能把本报告当成生产级 BFT 认证。磁盘回滚和复制验证者私钥仍可绕过本地防重复签名，部署需要额外控制。", "", "复现：`make consensus-evidence`。文件哈希只能核对文件变化，不提供第三方认证。", ""]
        if failure:
            lines += ["失败原因：" + str(failure), ""]
        (out / "report.md").write_text("\n".join(lines), encoding="utf-8")
        save(out / "manifest.json", {str(p.relative_to(out)): sha(p) for p in sorted(out.rglob("*")) if p.is_file() and p != out / "manifest.json"})
        print(f"{report['status'].upper()}: {out / 'report.md'}", flush=True)
    if failure:
        raise SystemExit(str(failure))


if __name__ == "__main__":
    main()
