# 交易提交、等待与确认

确认都要求 **所查询节点已验签、执行并完成区块持久化**。单节点和 authority/follower 模式返回 `confirmationScope:"local-durable"`；BFT 模式返回 `confirmationScope:"certificate-verified-local"`，还要求四个固定验证者中的至少三张有效预提交签名。两种回执都不意味着所有节点已经同步。

## 一条命令完成转账并等回执

```bash
./bin/settle transfer --to "$BOB" --amount 20 --wait --timeout 20s
```

成功输出 JSON 回执。不加 `--wait` 时保持原行为，只输出入池后的交易哈希。默认 nonce 取本节点待处理池中的第一个空位：同一个钱包向同一节点顺序发起多次转账，无需逐笔等出块再提交。余额不足的交易仍可能滞留，并发签名客户端必须自行管理 nonce，查询不会预留序号。

确认超时返回非零退出码，错误包含**已提交的交易哈希**。交易可能稍后确认，先查询原哈希：

```bash
./bin/settle wait --hash "$TX_HASH" --timeout 30s
./bin/settle rpc --method chain_getTransaction --params "[\"$TX_HASH\"]"
```

新 CLI 的自动 nonce 和等待功能需要升级后的 RPC 节点。不自动向旧接口降级，以免把另一种 nonce 或等待语义悄悄混入当前行为。

## 速度从哪里改善

- 回执直接查询持久化索引；单笔交易、账户、区块头查询只复制所需数据，不再复制整块或全部账户。
- Mempool 建立交易哈希索引；替换和确认时同步清理索引。
- 等待由提交事件唤醒，不再每 200ms 发一次 HTTP 查询。每次 RPC 等待最多 5s，整体时限由 CLI 控制。
- 默认每 100ms 检查出块条件；新交易触发 P2P 提前交换，保留周期重传作为补偿。

默认检查频率并不等于确认时限。区块时间戳仍使用 Unix 秒且严格递增，最多每秒一块；网络延迟、传播分页、待处理队列和磁盘速度都会影响确认时间。没有可执行交易时不出空块。authority 模式出块节点停机会暂停确认；BFT 模式可以自动换轮，但不足三个参与者时不确认。BFT 的投票传播和阶段超时还会影响时延，不能直接使用旧 authority 报告估计共识性能。

持久化仍逐块执行 fsync，写盘错误不会发成功回执。历史仍使用完整 JSON 快照，写入成本随历史增长；本轮优化没有解决这一存储扩展限制。

## 验证入口

```bash
make check          # 包括写盘故障、等待取消、并发等待、参数和容量边界
make benchmark      # 64 / 1000 笔区块内的单笔回执 HTTP handler 基准
make confirmation   # 本机四进程，6 批共 384 笔，逐笔时序和账本一致性
make evidence       # 故障恢复、离线审计与完整测试
```

前后对照实测（本地生成报告）区分了查询耗时和端到端确认时间，两者不应混写成 TPS。
