# SettleLab Release 路线图

阶段以验收结果推进，不承诺固定天数。每个阶段都应提供一条可重复的运行命令、正反例测试和可演示结果。

## V0.1 — Chain Core（已完成）

生成两个地址 → 签名转账 → 验签入池 → 打包 → 重执行校验 → 更新余额和 nonce → Merkle Proof 验证。验收入口：`go run ./cmd/demo` 和 `go test -race ./...`。

## V0.2-dev — Persistent Node / RPC（已完成）

持久化单节点、定时出块、JSON-RPC、钱包初始化与本地签名、交易回执和包含证明查询。验收入口：`make check`、`make smoke`。后者实际启动进程并验证已确认交易在 SIGKILL 后恢复。

项目展示名为 SettleLab，主线为可验证资产结算实验；内部 V0.1 哈希域保持兼容。

## V0.2-dev — P2P / Authority Sync（已完成）

libp2p TCP、独立网络身份、bootstrap、交易库存扇出及周期重传、固定 authority、分批区块同步、数据目录网络绑定、连接与错误查询。四进程验收入口：`make smoke-network`。当前为一个受信任出块节点和多个校验副本，不能称为 BFT。

## V0.2-dev — Offline Audit / Evidence（已完成）

离线签名、仅区块头 RPC、便携审计包导出、必须绑定外部可信区块哈希的离线验证器。`make evidence` 运行四进程负载与故障实验，保留源码指纹、逐交易时序、检查点和反例；首轮实测（本地生成报告）已经运行通过。

当前技术证据支持协议工程和审计参考实现的价值，未证明真实市场需求或生产容量。BFT 的优先级来自实测暴露的 authority 单点故障，而不是增加技术栈名词。

## V0.2-dev — Confirmation / Query Performance（已完成）

轻量区块头与账户查询、常数时间回执与待处理交易哈希索引、待处理 nonce、持久化提交事件通知、有界 RPC 长轮询、`transfer --wait`、100ms 出块检查和事件唤醒的 P2P 交换。保持整秒递增的区块时间戳和磁盘提交边界。速度对照报告（本地生成报告）记录实际前后结果；`make benchmark` 与 `make confirmation` 可重跑当前版本。

## V0.2 — BFT Consensus（实验网络与四进程故障验收已实现）

已实现四验证者签名投票、三票证书、轮次提议者轮换、POL 与持久化锁、防重复签名日志、带证书的原子区块提交和重放。`make consensus-demo` 使用四个进程内引擎与独立 fsync 目录演示六种故障，`make consensus-evidence` 保存完整验证证据。详见 [共识说明](consensus.md)。

已接入独立 libp2p 协议、分阶段自动超时、两成员签名轮次追赶，以及任意成员提供的三票证书分页同步。`settle bft-init` 初始化四验证者网络，`settlelab-node --validators ...` 启用。`make smoke-bft` 实测当前提议者离线自动恢复、两节点离线不确认、六块分页追赶和全部进程重启后继续交易。实验目录使用独立版本绑定，不能复用旧 authority 历史。

后续仍需跨机分区/延迟/丢包与恶意流量测试、一般调度下的活性分析、存储扩展和独立安全审计。

共识验收必须明确：验证者集合、带 chain/height/round/phase 的签名投票、每个验证者只计一票、冲突票拒绝、跨轮锁定规则、超时换主、落后节点同步和重启安全。四节点模型中法定人数为 3；仅有 Proposal / Prepare / Commit 消息流不能证明完整 PBFT 正确性。

演示分别覆盖：正常提交、一节点离线、恶意节点冲突投票、提议者离线、两节点失联时停止提交。分别说明安全性与活性依赖的网络假设。

## V0.3 — Infrastructure / Bridge（待实现）

加入可重放、幂等的 Indexer，使用 PostgreSQL 存储查询模型，Next.js 展示真实链上数据；Redis 仅在出现实际需求时加入。

Bridge 从本地 Anvil 开始。入金消息绑定来源链、合约、交易哈希与日志索引；处理确认策略、重组撤销和重复消息。提款需要可验证的授权与重放保护，不能由任意 relayer 单方面解锁。阈值多签与 MPC 是不同能力，完成多签不能宣称实现 MPC。

## V0.4 — Batch / ZK Experiments（待实现）

先演示批次排序、执行、数据提交和状态根锚定。仅向 L1 提交 StateRoot 不足以获得 Rollup 的执行正确性保证，应明确命名为 Batch / Commitment 实验。要进一步称为具备验证能力的 Rollup，需要补充有效性证明或欺诈证明，以及数据可用性与提款机制。

ZK 选择小型电路。余额阈值证明必须把私有余额绑定到已认可状态根下的账户承诺，约束整数位宽并处理范围；仅证明用户自选的私有数值大于阈值无法证明真实链上余额。普通 Merkle Proof 本身不等于 ZK。

最后补充 Docker Compose、可观测性和故障演示脚本；指标和简历描述只使用实际运行测得的数据。
