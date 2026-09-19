# AssetRail｜RWA 支付与交割 Layer 2

AssetRail 是面向 RWA 资产转移、支付和原子交割的应用专用 Layer 2 项目。Go 在链外执行交易，Solidity 在 L1 托管测试资产、接收批次数据、裁决执行争议并办理退出。

首版采用 **有界 Optimistic Rollup**：最多 8 个固定白名单账户、两种资产（测试支付代币与测试 RWA）、每批最多 32 笔操作及 32 笔充值。完整操作数据发布到 L1；任何人可提交带保证金的批次，任何人可在挑战期内以父状态为见证要求 L1 重执行。错误执行或错误状态根会被拒绝，保证金记入挑战者可领取余额。

本项目由 SettleLab 演进。原四验证者 BFT 实现作为独立的协议研究与回归模块保留；它的三票证书不参与新 L2 的 L1 最终结算，也不能代替执行争议验证。

## 场景与已实现能力

卖方存入测试 RWA，买方存入测试支付代币。双方共同签署一笔 DvP（券款对付）操作：RWA 从卖方转给买方，支付代币同时反向转移；任一余额、签名或 nonce 不正确则整个批次执行失败。

- 支付代币转账、RWA 转移、双签原子交割、两种资产的提款请求。
- Ethereum 地址、secp256k1 可恢复签名；域绑定 L1 chain ID、结算合约、操作内容、双方 nonce 和批次序号。
- L1 托管充值队列，按连续游标纳入执行，禁止重复记账。
- Go/Solidity 相同的整数规则、操作字节格式和状态根。
- L1 数据可用性、任何人可调用的有界全批次争议重执行、保证金惩罚。
- 挑战期后完成结算；提款固定发送给签名账户，禁止提前领取和重复领取。
- 停止结算后的全局冻结退出，以及未纳入结算的充值退款。
- 独立观察工具仅从 L1 重建账本、识别错误批次并生成挑战 calldata；重跑可处理 L1 重组。

## 一条命令验证闭环

需要 Go 1.21+、Python 3、Make，以及 Foundry 的 `forge`、`cast`、`anvil`。Solidity 编译器固定为 0.8.28，首次构建由 Forge 下载。macOS 需要 Xcode Command Line Tools；Makefile 保留外部链接和本地二进制签名兼容处理。

```bash
make assetrail-demo
```

该命令构建 Go CLI 和合约，启动临时本地 Anvil，部署两种测试代币及 L1 结算合约，然后执行：

1. 托管充值和双签 DvP，逐字节对比 Go 与 Solidity 执行结果。
2. 独立观察者发现伪造状态根，提交挑战并拒绝该批次。
3. 挑战期后提款，验证提前提款及重复领取被拒绝。
4. 普通支付、RWA 转移与支付代币提款。
5. 注入 L1 重组，丢弃已成为孤块的待结算批次。
6. 不使用节点数据库，从 L1 重建相同账本。
7. 停止结算，退出已结算余额并退回尚未处理的充值。

结果写入 `reports/assetrail/<UTC时间>/`，包含源码指纹、合约地址、交易哈希、状态根和场景结果。测试结束后清理 Anvil 进程与临时私钥。报告保持本地、不随源码上传。

```bash
make assetrail-test  # Go race 测试 + Solidity 单元/模糊测试
make check          # 所有 Go 模块，包括保留的 BFT 实现
```

## 操作入口

```bash
./bin/assetrail keygen --file alice.key
./bin/assetrail address --key alice.key
./bin/assetrail sign --config config.json --file order.json --key alice.key
./bin/assetrail sign --config config.json --file seller-signed.json --key bob.key --counter
./bin/assetrail batch --file batch.json
python3 scripts/assetrail_watch.py --rpc "$L1_RPC" --contract "$ROLLUP" --out snapshot.json
```

观察工具默认读取 L1 的 `finalized` 区块；本地 Anvil 验收使用 `--block latest`。它默认只读，不持有私钥、不自动发送公网交易。发现未结算错误批次时退出码为 2，结果含 `challengeData`；实际监控必须持续运行并在挑战期内提交挑战。

完整格式、部署和状态阶段见 [运行指南](docs/assetrail.md)；安全假设与限制见 [协议说明](docs/assetrail-protocol.md)。

## 能力边界

- 当前提供测试资产和本地 EVM 验收，没有真实 RWA 权属、储备、收益或兑付接入。
- 固定账户名单只是技术准入，不是 KYC 服务或完整 ERC-3643 实现；没有动态身份撤销和冻结传播。
- 安全依赖至少一个诚实观察者及时挑战、L1 可访问且数据可获取。挑战窗口内的 L2 执行结果不是最终结算。
- 首版是有限账户、有限批次的全状态争议验证，没有通用 EVM、ZK 电路、交互式单步欺诈证明或可扩展状态树。
- 全局逃生解决停止结算，尚未提供持续出批期间的强制包含或单用户强制退出。固定小额测试窗口不能用作生产配置。
- 仅支持无转账税、无 rebase 的测试资产。RWA 发行方的链外兑付与法律权利不由 L2 证明保证。
- 尚无外部安全审计、公开测试网部署、生产性能或费用节省结论。

## 代码入口

| 路径 | 职责 |
| --- | --- |
| `rollup/types.go` | 多资产状态、签名域、Ethereum 地址与固定操作编码 |
| `rollup/execute.go` | 充值、支付、DvP、提款的确定性原子执行 |
| `cmd/assetrail/` | 钱包、签名、批次执行和解码 CLI |
| `contracts/src/AssetRail.sol` | L1 托管、批次、争议、结算、提款和逃生 |
| `contracts/src/MockAsset.sol` | 测试支付资产和静态准入测试 RWA |
| `scripts/assetrail_watch.py` | 独立 L1 数据重放与挑战 calldata |
| `scripts/assetrail_demo.py` | 真实 Anvil + Go + Solidity 端到端验收 |
| `consensus/`、`bft/` | 保留的 SettleLab 四验证者共识，不是 L2 结算依据 |

旧协议的哈希域、数据目录和 CLI 保持兼容，不把旧账本自动迁移成有 L1 资产担保的余额。旧运行方式见 [SettleLab 存档](docs/legacy-settlelab.md)。Go module 暂保留 `gochain`，避免名称变更破坏内部导入。

CI 配置以 [.github/ci.yml.example](.github/ci.yml.example) 模板保存；当前 GitHub 令牌缺少 `workflow` 权限，因此尚未启用 Actions。公开源码不自动公开本地报告或私钥。
