# 实验共识网络：四验证者，两阶段投票

当前提供 **签名共识核心、真实 libp2p 投票传播、自动分阶段超时、证书同步和四进程故障测试**。四个固定、等权重的 Ed25519 验证者，需要至少三个不同验证者对同一高度、同一轮次、同一区块预提交，才能形成提交证书。

`settlelab-node --validators <配置文件> --p2p-key <验证者密钥>` 启用 `bft-experimental` 模式。单节点与 authority/follower 模式继续可用，确认语义分别保留；BFT 必须使用独立数据目录。

## 启动四个真实节点

```bash
make build
./bin/settle bft-init --dir .settlelab-bft

# 生成四套验证者密钥及共同配置；默认 P2P 端口 9100..9103。
# 启动四个独立进程，RPC 使用 18545..18548。
for i in 0 1 2 3; do
  ./bin/settlelab-node \
    --genesis .settlelab-bft/genesis.json \
    --validators .settlelab-bft/validators.json \
    --p2p-key ".settlelab-bft/validator${i}.key" \
    --data ".settlelab-bft/data${i}" \
    --listen "127.0.0.1:$((18545+i))" \
    > ".settlelab-bft/node${i}.log" 2>&1 &
  echo $! > ".settlelab-bft/node${i}.pid"
done

BOB=$(./bin/settle address --key .settlelab-bft/bob.key)
./bin/settle transfer --rpc http://127.0.0.1:18545 \
  --key .settlelab-bft/alice.key --to "$BOB" --amount 20 --wait --timeout 40s
./bin/settle rpc --rpc http://127.0.0.1:18545 --method chain_getPeers
./bin/settle rpc --rpc http://127.0.0.1:18545 \
  --method chain_getCommitCertificate --params '["latest"]'

# 实验结束后停止上述四个进程。
for i in 0 1 2 3; do kill "$(cat ".settlelab-bft/node${i}.pid")"; done
```

`bft-init` 拒绝覆盖已有目录；可用 `--ports 9200,9201,9202,9203` 更换 P2P 端口。共享的 `validators.json` 绑定 chain ID、创世块和四个公开验证者身份；每个 `/p2p/` Peer ID 必须对应该条 Ed25519 公钥。钱包私钥和验证者私钥独立。跨机器部署需事先分发同一公开配置和创世文件，每个节点只持有自己的验证者私钥。

`--consensus-timeout` 默认 2s，是每个阶段的初始超时，允许 500ms..10s。`--p2p-interval` 默认 200ms，BFT 允许 50ms..5s；`--p2p-listen` 未提供时采用配置中的本机地址。BFT 模式拒绝同时设置 `--authority` / `--peers`；`--block-time` 不控制 BFT 提交。`/healthz` 只表示本地服务健康，不表示存在三票法定人数。

## 真实进程验收与核心演示

```bash
make smoke-bft             # 四独立 OS 进程 + TCP/libp2p + 实际超时 + SIGKILL
make bft-evidence          # 同一验收入口的别名
```

四进程报告写入 `reports/bft/<UTC时间>/`：正常确认、当前提议者 SIGKILL 后自动换轮、离线六块后的分页追赶、只剩两节点时无误确认、恢复法定人数和全体重启后继续交易。保存 PID、源码/二进制指纹、公开配置、日志、状态根、回执和签名证书；临时钱包/验证者私钥和数据目录会清理。这是本机故障验证，不是跨机网络压力测量。

本次四进程验收报告（本地生成报告）通过六项场景并记录 14 个账本检查点；完整 race 测试日志（本地生成报告）与旧模式回归日志另行保存。

以下保留进程内核心演示，用于控制冲突提案、丢弃特定阶段投票和锁定后重启：

```bash
make consensus-demo       # 六个故障场景，输出证书和落盘目录
make consensus-evidence   # 完整无缓存 race 测试 + 六场景 + 源码和证据文件指纹
```

可指定演示输出目录，要求该目录尚不存在：

```bash
go run ./cmd/consensus-demo --out /tmp/settlelab-consensus-example
```

本机旧 Go / 新 macOS 组合需要 `go run -ldflags=-linkmode=external ...`；Makefile 已包含这一处理。

演示通过后，各项显示 `passed=true`。报告输出到 `reports/consensus/<时间>/`。实际使用四个签名引擎、四份独立节点与落盘目录，执行真实交易验签、状态变更和 fsync；消息投递、掉线与超时由进程内驱动控制。它不是四个独立操作系统进程，也没有测量 P2P 网络共识性能。私钥只保留在本次进程内，结束后这些目录仅用于检查证据。

本次完整验证报告（本地生成报告）保留六场景结果、测试日志、签名证书与源码指纹。

## 协议与锁定规则

1. **提案**：按排序后的验证者公钥，以 `(height - 1 + round) mod 4` 轮换提议者。提案带签名；有已知有效区块时，优先重提该区块并附上较早轮次的三票预投票证明 POL。
2. **预投票 Prevote**：节点重新执行候选块，检查父块、交易签名、状态根。未锁定、提案与锁定块相同，或提案带有轮次不低于本地锁定轮的有效 POL 时，可以投该区块；否则投 nil。
3. **预提交 Precommit**：收齐当前轮针对同一区块的三票预投票后，保存有效块和锁，再签预提交。保存失败不发送投票。三票 nil 预投票只会产生 nil 预提交，不形成区块决定。
4. **决定与提交**：三票非 nil 预提交形成决定。节点验证证书，并重新执行区块；完成区块和证书的持久化后才发布回执。
5. **超时换轮**：`Timeout(height, round)` 忽略过期回调，否则递增轮次并轮换提议者，保留锁和有效块。清空当前轮的提案、投票收集器，绝不清空安全锁。

签名域 `settlelab/consensus-* /v1` 区分验证者集合、投票和提案。集合哈希绑定链 ID、创世块哈希和排序后的四个公钥；投票再绑定高度、轮次、阶段、区块哈希和签名者。同一人重复签名只能计一票；不同阶段、轮次、高度或集合的签名不能混成证书。

这是参考 [The latest gossip on BFT consensus](https://arxiv.org/abs/1807.04938) 的实验子集，而非对论文算法或 CometBFT 的完整实现。两组三票在四人集合中至少重叠两人；在至多一名拜占庭参与者的假设下，交集中至少有一名诚实参与者。跨轮安全还依赖锁定规则与持久化签名约束，不能仅靠“三票门槛”推导整个实现正确。

## 持久化和恢复

- `network.json` 使用独立版本 2，绑定本地验证者 ID 与验证者集合哈希。旧 authority 模式仍使用版本 1。必须使用新数据目录，禁止把已有 solo/authority 历史原地当成共识历史。
- `consensus.json` 保存当前高度、轮次、已签提案、预投票、预提交、锁、有效块/POL和决定。临时文件写入、文件 fsync、rename 和目录 fsync 完成后，签名才交给调用方广播。
- 存储层也检查转换：不允许高度/轮次回退、同轮改票、覆盖已签提案、遗忘锁或替换决定。两个过期引擎不能靠顺序覆盖文件绕过本地防重复签名。
- `chain.json` 在同一原子快照中保存区块及证书，启动重放时逐块重新验签、执行和检查证书。不能把“收到三票”直接当成“本地已落盘”。
- 决定先写安全日志，再写区块。若在两者之间退出，重启后使用日志中的决定继续 `CommitCertified`；若账本已提交而日志仍在上一个高度，则检查决定后进入下一高度。
- 已绑定目录的安全日志缺失时拒绝打开，不会自动创建空投票记录。写盘错误后停止服务，需要重新打开并检查已有状态。

这些保证依赖文件系统的 fsync/原子替换语义和验证者密钥不被复制。手工回滚完整目录、恢复过期备份或把同一私钥运行在不同目录，可能绕过本地防重复签名；当前没有远程签名器、硬件单调计数器或防回滚备份协议。

## 节点与集成 API

核心入口在 [consensus/engine.go](../consensus/engine.go)，节点适配在 [node/consensus.go](../node/consensus.go)，完整消息驱动示例在 [internal/bftdemo/demo.go](../internal/bftdemo/demo.go)。

```go
// publicKeys 必须是事先认可的四个公钥，genesisHash 来自同一创世配置。
set, err := consensus.NewValidatorSet(genesis.ChainID, genesisHash, publicKeys)
// 检查 err 后：
n, err := node.OpenConsensus(dataDir, genesis, validatorID, set)
// 检查 err 后：
engine, err := n.NewConsensusEngine(privateKey)
```

驱动负责将交易传播到池、调用 `Candidate` / `Propose`，投递 `ReceiveProposal` 和 `ReceiveVote` 的输出，广播完整提交证书，并调用 `CommitCertified`。`NewConsensusEngine` 限制单节点同时只有一个活跃签名核心；提交后可为下一高度创建新核心，旧核心停止签名。

`CommitCertified` 可从任何来源接收完整证书，信任依据是本地固定验证者集合与验签，不再依据一个 authority 的传输身份。仍需逐个高度导入并验证父块，不允许跳过执行历史。候选创建不改变账本；普通 `Produce` 在实验 BFT 节点上不出块，authority `Import` 入口拒绝使用。

实验节点的回执使用 `confirmationScope:"certificate-verified-local"`，表示证书已验证且本地持久化完成。挂载现有 `rpc.New(n)` 后可调用 `chain_getCommitCertificate`，参数为高度或 `"latest"`；返回 3..4 个可单独验签的投票，高度和轮次均为十进制字符串。旧模式或未知高度返回 null。

审计包目前仍是“外部可信区块哈希 + 单笔交易包含证明”，没有自动打包验证者配置与提交证书。不能把既有 `settle verify` 的成功输出解释成完成了共识证书验证。

## 已覆盖的核心故障

下表描述进程内可控调度与单元测试；真实进程故障由前述 `make smoke-bft` 另外验证。

| 场景 | 验收行为 |
| --- | --- |
| 正常确认 | 四节点连续提交两个高度，重启后余额、nonce、块哈希和证书一致 |
| 一个非提议者离线 | 其余三个确认，恢复节点凭证书补齐 |
| 提议者离线 | 注入逻辑超时，轮换后由剩余三个确认 |
| 两节点离线 | 四个逻辑轮次内无确认、无成功回执 |
| 锁定后全员重启 | 丢弃预提交消息后重启，保留锁，携带 POL 重提原块 |
| 恶意提议者签出两个提案 | 分流票不虚假确认，冲突提案拒绝，三名诚实参与者换轮恢复 |
| 证书或日志异常 | 重复票、错域、错轮、错阶段、签名篡改、损坏证书和缺失安全日志均拒绝 |
| 存储故障 | 日志写失败不发票，区块写失败不发回执；已保存决定可以恢复提交 |

## 网络与自动换轮

- 独立协议 `/settlelab/bft-exchange/1.0.0`，只接受固定集合内的认证 Peer ID。三个对端各自运行交换循环，失败退避最长 2s，不等待掉线对端后才向其他节点传播。
- 反复交换当前提案、最多八张当前轮投票、最多四条签名轮次通知及最多 32 笔交易。收到的合法投票也会转发，重传修复丢失消息和重启后丢失的内存投票。单帧最多 4 MiB，入站处理最多八路，每条流有 3s 截止时间；未知字段和错误域拒绝。
- 提案阶段到期签 nil prevote；预投票阶段到期签 nil precommit；预提交阶段到期进入下一轮。已有签名不会因超时改票。每次高度、轮次或阶段改变后重设定时器，阶段超时为 `base + round × base / 2`，检查周期为 50ms。即使没有交易也可能轮换，但不会产生空区块。
- 使用单独签名域的 `RoundNotice` 广播已持久化的高度/轮次。收到至少两个不同验证者的有效通知时，追赶至至少两人支持的最高轮次（轮次排序后的第二大值），保留锁/POL。单个异常验证者不能独自造成大幅跳轮。通知不是投票，不计入提交证书。
- 历史同步每页最多四个连续“区块＋证书”。检查祖先哈希、父块连续性、每张证书和交易执行后，逐块持久化；可以从任何配置成员拉取。不能仅凭传输身份导入区块。
- 实时提案时间戳不得超过本机时间 5s，避免接受遥远未来的时间戳后阻塞正常出块；这引入时钟偏差假设。历史证书同步不以接收时钟重新决定共识结果。

当前核心只保留当前高度/轮次的单票。未来轮由签名通知促成追赶后重传，完整旧轮证书仍可用于当前高度的决定。没有照搬论文的完整未来消息队列或一般活性证明。

## 后续边界

仍需跨机器分区/延迟/丢包与恶意成员资源消耗测试、更多调度下的锁定恢复验证、性能测量和独立安全审计。未实现动态成员切换、远程防重复签名服务和增量存储。

验证者集合固定且无权重、无动态成员切换。高度和轮次不得溢出，轮次到 1,000,000 后停止推进。没有一般异步调度下的活性证明或生产安全审计，当前定位是可运行、可复现的实验网络，不宣称生产就绪。
