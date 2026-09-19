> 历史模块存档：以下为独立 SettleLab BFT 链的运行方式。新 RWA 支付 L2 请阅读 [AssetRail](../README.md)。两者账本、地址与最终结算机制不同。

# SettleLab｜可验证资产结算实验链

使用 Go 构建的资产结算实验平台。业务主线是：用户签名转账 → 节点确认账本 → 查询结算回执与包含证明 → 后续扩展多节点验证和跨链充值提现。

当前版本 **V0.2-dev：四验证者实验共识网络**。支持真实签名投票传播、三票证书确认、自动超时换轮、分页追块和崩溃恢复。使用 `--validators` 启用 BFT 模式；原单节点与固定 authority/follower 模式仍可运行。

已支持**离线交易审计与可复现实测报告**：导出签名交易、区块头和 Merkle 路径，在外部可信区块哈希下离线验证。四进程实验覆盖负载、篡改、重放和停机恢复，并保存原始时序与源码指纹。

**项目价值从证据开始：** [价值与边界](value.md) · 已运行的实测报告（本地生成报告） · [离线审计使用说明](audit.md)。本轮 64/64 笔确认，审计包比对应完整区块小 96.3%；数据是本机受控实验结果，不能外推为生产容量。

本轮继续完善**确认与查询速度**：新增 `transfer --wait`、服务端事件驱动的回执等待、待处理 nonce 查询、常数时间回执索引，以及交易入池后的加速传播。详见 [确认语义与操作](confirmation.md) 和 速度对照报告（本地生成报告）。

新增 **真实四进程共识故障验收**：`make smoke-bft` 验证提议者离线自动换轮、六块分页追赶、失去三票法定人数时无确认，以及恢复与全体重启。启动命令见 [共识网络说明](consensus.md)，验收证据见 四进程报告（本地生成报告）；`make consensus-demo` 保留用于冲突提案和锁定恢复的六场景核心演示。

## 快速运行

需要 Go 1.21+、Make；持久化节点支持 macOS / Linux。首次构建需联网下载 secp256k1 依赖。macOS 需要 Xcode Command Line Tools；Makefile 使用外部链接并对生成的本地二进制执行 ad-hoc 签名，兼容本机旧 Go 与新 macOS 的组合，不修改全局 Go 或系统设置。

```bash
make build

# 创建本地开发钱包及创世配置；已存在的目录会拒绝覆盖
./bin/settle init

# 终端一：启动服务，默认每 100 毫秒检查一次可执行交易（协议仍最多每秒一块）
./bin/settlelab-node
```

终端二：

```bash
ALICE=$(./bin/settle address --key .settlelab/alice.key)
BOB=$(./bin/settle address --key .settlelab/bob.key)

# 初始 Alice=1000000、Bob=0；本地签名，私钥不发送到节点
TX_HASH=$(./bin/settle transfer --to "$BOB" --amount 20)

# 等待实际提交，然后查看回执
./bin/settle wait --hash "$TX_HASH"

# 查询 Bob 余额，应为字符串 "20"
./bin/settle rpc --method chain_getBalance --params "[\"$BOB\"]"

# 查询交易所在块的 Merkle 包含证明
./bin/settle rpc --method chain_getTransactionProof --params "[\"$TX_HASH\"]"

# 节点高度、状态根和 Mempool 状态
./bin/settle rpc
```

在终端一按 Ctrl+C，再运行 `./bin/settlelab-node`。节点重新校验磁盘中的历史区块，余额、nonce、区块哈希、回执与证明查询均恢复。重复运行 `init` 不会覆盖原有钱包和配置。

无可执行交易时不产生空块。默认 `transfer` 使用 Alice 钱包、GasPrice=0，并从当前节点待处理交易中寻找下一个可用 nonce，支持顺序连续提交。多个并发签名客户端仍需协调 nonce；查询不会预留 nonce，节点间待处理池也可能暂时不同。替换交易使用同一 nonce 和更高的 `--gas-price`。

## 当前功能

| 模块 | 已实现 |
| --- | --- |
| 钱包 | 随机 secp256k1 密钥、本地签名、0600 文件权限、不覆盖已有密钥 |
| Transaction | 固定字段二进制哈希、ECDSA 验签、规范 DER / low-S、Chain ID 防错链重放 |
| Mempool | Nonce 排队、至少 10% 加价替换、容量限制、按价格和 nonce 选择可执行交易 |
| 状态机 | 余额、nonce、自转账、固定 Gas、费用销毁、溢出检查 |
| 区块 | 父哈希、TxRoot、StateRoot、整块重执行校验、失败回滚 |
| 持久化 | 临时文件写入、fsync、原子替换、目录锁、启动重放及损坏历史拒绝 |
| 节点 | 定时出块、串行协调入池与提交、优雅退出、写盘异常后停止服务 |
| P2P | libp2p TCP、持久化 Ed25519 身份、静态 bootstrap、交易扇出与周期重传 |
| 同步 | authority 同步或 BFT 三票证书同步、祖先检查、分批拉块、逐块执行/持久化 |
| 共识 | 四等权验证者、两阶段签名、持久化锁/POL、自动超时、认证换轮追赶 |
| RPC | JSON-RPC 2.0、批量请求、通知、余额/nonce/区块/交易/回执/证明查询、有界回执长轮询 |
| 确认体验 | `transfer --wait`、待处理 nonce 接续、持久化后通知、超时保留交易哈希 |
| 离线审计 | 证据包导出、外部区块哈希绑定、离线授权与包含证明验证、篡改拒绝 |
| 工程 | 单元测试、HTTP 集成测试、race 检测、真实进程强制退出恢复测试、CI |

未来场景是多个参与方共同维护账本，并接入测试网资产充值提现。当前验证交易传播、各节点账本一致性及可恢复的结算记录，并提供可运行的四验证者实验 BFT 网络。尚无 Bridge、Indexer、Explorer、Rollup 或 ZK 电路，也未完成生产安全审计。

## 验证

公开仓库包含源码、测试脚本和文档。历史运行报告保留在本地，不随源码发布；下文引用的历史数字来自本地实验。执行对应验收命令后，会在被 Git 忽略的 `reports/` 目录生成本次报告、日志和证据。


```bash
make smoke-bft    # 真实四进程 BFT 投票、自动换轮与 SIGKILL 故障报告
make consensus-demo # 四签名引擎与独立落盘状态，六个逻辑故障场景
make consensus-evidence # 无缓存完整测试 + 共识证据报告
make benchmark   # 回执 RPC 查询基准，3 次测量
make confirmation # 四进程、6 批共 384 笔确认时序实验
make check       # go vet + go test -race
make smoke       # 构建真实二进制并执行端到端场景，需要 Python 3
make smoke-network # 四个独立进程：传播、追块、停机和恢复，需要 Python 3
make evidence    # 全量验收、64 笔负载及审计/故障报告，需要 Python 3
make demo        # 保留原 V0.1 内存闭环演示
```

`make smoke` 在临时目录创建开发链，启动真实 HTTP 节点、发送签名交易、等待持久化回执、查询证明，随后 SIGKILL 节点并重启，验证恢复结果，再完成第二笔转账；退出时清理测试进程和临时数据。

`make smoke-network` 运行一个 authority 和三个 follower：从 follower 提交转账、核对四份状态根，停止并重启一个 follower 验证追块，再停止 authority 验证结算暂停，恢复 authority 后处理期间提交的交易。测试自动清理其临时目录和进程。

`make evidence` 先执行 `go vet` 与不使用测试缓存的 race 测试，再运行四进程实验。每轮结果写入新的 `reports/evidence/<UTC时间>/`，包括报告、逐交易时序、检查点、审计包、篡改反例、日志和文件校验清单；临时私钥不保留。CLI 验真命令及信任模型见 [审计文档](audit.md)。

## GitHub Actions

CI 配置保留在 [.github/ci.yml.example](../.github/ci.yml.example)，公开仓库暂未启用自动运行。本地可以执行 `make check` 和 `make smoke-bft` 完成验证。

启用时将模板复制到 `.github/workflows/ci.yml`，并强制添加这个当前被忽略的文件：`git add -f .github/workflows/ci.yml`。通过 HTTPS 推送工作流需要 GitHub 登录令牌具有 `workflow` 权限；也可在 GitHub 网页端创建该工作流文件。

## 手动运行 authority 多节点

在 `settle init` 创建的目录中生成独立网络身份（只需一次，密钥文件不会覆盖）：

```bash
./bin/settle identity --out .settlelab/authority.p2p.key
./bin/settle identity --out .settlelab/follower.p2p.key
```

终端一启动出块节点。以下数据目录与原来的单节点历史分开：

```bash
AUTHORITY_ID=$(./bin/settle peer-id --key .settlelab/authority.p2p.key)
./bin/settlelab-node --data .settlelab/authority-data \
  --p2p-key .settlelab/authority.p2p.key --authority "$AUTHORITY_ID" \
  --p2p-listen /ip4/127.0.0.1/tcp/9000 --listen 127.0.0.1:8545
```

终端二启动同步节点：

```bash
AUTHORITY_ID=$(./bin/settle peer-id --key .settlelab/authority.p2p.key)
./bin/settlelab-node --data .settlelab/follower-data \
  --p2p-key .settlelab/follower.p2p.key --authority "$AUTHORITY_ID" \
  --p2p-listen /ip4/127.0.0.1/tcp/9001 --listen 127.0.0.1:8546 \
  --peers "/ip4/127.0.0.1/tcp/9000/p2p/$AUTHORITY_ID"
```

终端三通过同步节点提交交易并查询连接：

```bash
BOB=$(./bin/settle address --key .settlelab/bob.key)
TX_HASH=$(./bin/settle transfer --rpc http://127.0.0.1:8546 --to "$BOB" --amount 20)
./bin/settle wait --rpc http://127.0.0.1:8546 --hash "$TX_HASH"
./bin/settle rpc --rpc http://127.0.0.1:8546 --method chain_getPeers
```

更多节点使用不同身份、RPC/P2P 端口和数据目录，共享创世配置与 authority。网络配置会绑定数据目录；重启必须提供原身份与 authority，避免意外开启独立出块或接入另一条网络。完整信任模型、限制和协议见 [P2P 说明](p2p.md)。

## 运行参数

```bash
./bin/settlelab-node \
  --genesis .settlelab/genesis.json \
  --data .settlelab/data \
  --listen 127.0.0.1:8545 \
  --block-time 2s

./bin/settle transfer \
  --rpc http://127.0.0.1:8545 \
  --key .settlelab/alice.key \
  --to "$BOB" --amount 20 --nonce 1 --gas-price 0
```

RPC 默认绑定本机地址，`GET /healthz` 返回健康状态。节点没有钱包解锁、远程签名或远程挖块接口。本版本未提供浏览器客户端，拒绝带 Origin 的 RPC 请求，不启用 CORS。

## 协议与持久化约定

- 金额都是 `uint64` 最小整数单位。RPC 中金额、Gas、nonce、高度等使用**十进制字符串**，不通过 JavaScript Number 传递。
- 地址是 SHA-256(压缩公钥) 最后 20 字节，使用小写 `0x`；交易采用自定义格式，不兼容 Ethereum 地址和 RLP。
- 单次转账固定消耗 21,000 Gas；要求 GasLimit ≥ 21,000，实际费用为 `21,000 × GasPrice` 并销毁。演示允许零费用。
- `Nonce` 是下一笔交易的 nonce，从 0 开始。余额必须覆盖金额加费用；自转账最终只扣费用。
- Mempool 的 pending 表示 nonce 连续，queued 表示有间隔；能否支付由候选块模拟执行决定。替换至少增加 `max(1, ceil(oldPrice/10))`。
- `committed` 表示本节点已校验并持久化。solo/authority/follower 的 scope 为 `local-durable`；BFT 为 `certificate-verified-local`，还要求三票提交证书。原 authority 模式停机不会自动换主，BFT 模式由分阶段超时轮换提议者。
- 区块与创世配置持久化，状态与回执索引从历史重建。Mempool 暂不持久化，重启后需要重新提交未确认交易。
- 当前每次提交重写完整历史快照，时间和磁盘写入量为 O(历史大小)；节点内存也保留完整历史。适合本地实验，后续再换增量存储。
- 普通 Merkle Proof 是交易包含证明，不是零知识证明，也不单独证明一份根来自可信共识。

项目展示名已调整为 **SettleLab**。Go module 暂保留 `gochain`，V0.1 的 `gochain/tx/v1` 等哈希域保持不变，避免名称调整改变交易与区块身份。

## 目录与文档

```text
cmd/demo/       内存闭环演示
cmd/node/       持久化节点服务
cmd/settle/     开发链初始化、钱包、转账与 RPC 客户端
audit/          不依赖网络的签名交易包含证明验证器
blockchain/     Transaction、State、Block、Chain、Genesis
crypto/         secp256k1、SHA-256、Merkle Tree / Proof
mempool/        Nonce 队列与候选交易选择
storage/        原子快照及独占目录锁
node/           节点协调、持久化提交、回执索引
p2p/            身份、authority 模式交易传播与分批同步
consensus/      签名投票、锁/POL、安全日志、签名轮次通知
bft/            四成员认证网络、真实投票交换、自动超时和证书同步
rpc/            HTTP 服务、客户端及大整数编码
wallet/         本地密钥读写
scripts/        真实进程端到端验证
```

- [RPC 接口与调用约定](rpc.md)
- [离线审计与信任锚](audit.md)
- [项目价值、证据与边界](value.md)
- [多节点网络与信任模型](p2p.md)
- [协议编码与存储模型](protocol.md)
- [后续路线图](roadmap.md)

密码学使用 [Decred secp256k1](https://github.com/decred/dcrd/tree/master/dcrec/secp256k1)，HTTP 协议依据 [JSON-RPC 2.0 规范](https://www.jsonrpc.org/specification)。
