# AssetRail 运行指南

## 本地完整验收

安装 Go 1.21+、Python 3、Make 和 Foundry（`forge`、`cast`、`anvil`），在仓库根目录运行：

```bash
make assetrail-demo
make assetrail-test
```

第一条命令部署真实 Solidity 合约到临时 Anvil，运行充值、双签交割、挑战、两种资产提款、重组恢复与逃生。每个步骤包含断言；失败返回非零退出码。报告在 `reports/assetrail/`，不会被提交到 Git。金额均为代币最小单位，示例 1000 不表示 1000 美元；测试代币显示精度为 6。

演示结束后会停止 Anvil 并删除临时私钥，因此报告中的合约地址不能当作持续运行的网络使用。原 `settlelab-node` 的 8545 页面仍属于旧链，不是新 L2 的入口。新入口是 `bin/assetrail`、L1 合约和独立观察脚本。

## 持续运行一个开发网络

在一个终端启动 `anvil --host 127.0.0.1 --chain-id 31337`。使用独立开发钱包和本地 RPC；`scripts/assetrail_demo.py` 的部署顺序可作为可执行参考。合约构建产物位于 `contracts/out/`。

部署顺序及构造参数：

1. `MockAsset("Test Payment", "tPAY", false)`。
2. `MockAsset("Test RWA", "tRWA", true)`。
3. `AssetRail(cashToken, rwaToken, members, challengePeriod, exitDelay, proposerBond)`，其中 members 为 1–8 个不重复 Ethereum 地址，`exitDelay >= 2 * challengePeriod`，bond 大于零。
4. 测试 RWA 发行者调用 `admit(address)`，准入结算合约及参与人，然后 `mint(address,uint256)`。支付代币也仅由发行者 mint。

可用 `forge create --root contracts src/MockAsset.sol:MockAsset --rpc-url "$L1_RPC" --account dev --broadcast --constructor-args "Test Payment" tPAY false` 部署第一个代币；其余按上述构造参数部署。`dev` 是自己导入的 Foundry 开发钱包，不是仓库提供的账户。不要把私钥写入命令或提交到仓库。

本地验收使用 60 秒挑战期、300 秒停止结算退出期及 0.01 ETH 保证金。这些数值只为快速测试，尚未完成公网参数评估。新合约不可升级；新部署不会继承旧 SettleLab 账本。

## 配置与签名

```bash
./bin/assetrail keygen --file seller.key
./bin/assetrail keygen --file buyer.key
./bin/assetrail address --key seller.key
```

保存 `config.json`，地址必须替换为本次部署的真实地址，全部小写，accounts 顺序必须与构造参数一致。`l1ChainId` 使用十进制字符串。

```json
{
  "l1ChainId": "31337",
  "contract": "0x1111111111111111111111111111111111111111",
  "accounts": [
    "0x2222222222222222222222222222222222222222",
    "0x3333333333333333333333333333333333333333"
  ]
}
```

双方分别 approve 结算合约。买方调用 `deposit(1, 0, 1000)`，卖方调用 `deposit(0, 1, 100)`。资产编号 0 为支付代币、1 为 RWA；充值队列按 L1 顺序处理。允许替他人充值，尚未纳入结算的退款归实际付款人。

卖方发出以下 `order.json`：卖出 10 个最小单位 RWA，交换 250 个最小单位支付资产。`price` 是总付款额，不是单价。假定这是第一个批次，双方 nonce 都为零。

```json
{
  "kind": 2, "from": 0, "to": 1,
  "amount": "10", "price": "250",
  "nonce": "0", "counterNonce": "0", "sequence": "1",
  "signature": "", "counterSignature": ""
}
```

```bash
./bin/assetrail sign --config config.json --file order.json --key seller.key > seller-signed.json
./bin/assetrail sign --config config.json --file seller-signed.json --key buyer.key --counter > signed.json
```

签名命令只签署并输出 JSON，不发送交易。输入与输出应使用不同文件，避免 shell 重定向先清空输入。该协议签的是自定义原始摘要，不可直接用钱包的 `personal_sign` 代替；尚未接入浏览器钱包。

## 批次执行、提交与领取

`batch.json` 包含 `config`、`previous`、`sequence`、`depositStart`、`deposits`、`operations`。以下 Python 示例将上述文件组合成第一个批次；充值顺序必须与合约队列一致，不能任意构造增加余额。

```bash
python3 - <<'PY'
import json
batch = {
    'config': json.load(open('config.json')),
    'previous': {k: ['0'] * 8 for k in ('cash', 'rwa', 'nonce')},
    'sequence': '1', 'depositStart': '0',
    'deposits': [
        {'account': 1, 'asset': 0, 'amount': '1000'},
        {'account': 0, 'asset': 1, 'amount': '100'}],
    'operations': [json.load(open('signed.json'))]
}
with open('batch.json', 'w') as f:
    json.dump(batch, f, indent=2)
PY
./bin/assetrail batch --file batch.json > result.json
```

执行结果包含 `preRoot`、`postRoot`、`state`、`data`、`depositEnd`、`sequence`。提交者从 L1 确认父根和充值游标后，带上准确 bond 调用 `propose(result.data, result.depositEnd, result.postRoot)`。一次只能存在一个待结算批次。

| 阶段 | 含义 | 后续操作 |
| --- | --- | --- |
| 本地执行成功 | Go 校验通过，尚未发布到 L1 | 提交 propose |
| L1 待结算 | 数据可读，挑战窗口尚未关闭 | 独立重执行并挑战错误批次 |
| L1 合约已 finalize | 挑战期已过，合约接受根 | 等待所在 L1 区块的最终性 |
| L1 区块最终确认 | 在 L1 安全假设下结算完成 | 领取已签署提款 |

窗口到期后任何人调用 `finalize()`。提款操作同样先进入批次并扣除 L2 余额，结算后调用 `claimWithdrawal(batchId, operationIndex)`；资金固定发给原签名账户，调用者不能改收款人。保证金退款或挑战奖励通过 `withdrawCredit()` 领取。被拒绝批次不推进 sequence，诚实提交者可重用该序号重提有效内容。

| kind | 操作 | from / to | 额外要求 |
| --- | --- | --- | --- |
| 0 | 支付代币转账 | 付款人 / 收款人 | price、counterNonce 为零，副签名留空 |
| 1 | RWA 转移 | 转出人 / 接收人 | 同上 |
| 2 | DvP | 卖方 / 买方 | 双方签名与 nonce；amount 为 RWA 数量，price 为付款总额 |
| 3 | 支付代币提款 | 两者相同 | 无副签名 |
| 4 | RWA 提款 | 两者相同 | 无副签名 |

所有数量、nonce、批次序号为 uint64 十进制字符串；账户索引和 kind 为 JSON 数字。不要使用浮点数。后续批次以观察工具重建的已结算 state、sequence + 1 和 depositCursor 为起点。

## 独立观察与退出

```bash
python3 scripts/assetrail_watch.py --rpc "$L1_RPC" --contract "$ROLLUP" --out snapshot.json
# 仅在受控本地网络测试未最终确认的区块：
python3 scripts/assetrail_watch.py --rpc "$L1_RPC" --contract "$ROLLUP" --block latest
```

脚本每次从 L1 创世状态重放，固定读取同一区块并复核其哈希。默认 `finalized`，RPC 必须支持该标签和对应历史 eth_call。成功退出码为 0；发现待结算欺诈为 2，并返回 `pending.challengeData`；无法验证或发现已结算无效状态则失败，不输出伪造的可用余额。输出中的 state 是 L2 账本状态，逃生和提款后的 L1 资产到账应另查代币余额与事件。

工具是单次只读检查，**不是已部署的自动挑战服务**。运营者需要持续检查，在截止时间前将 challengeData 发往正确合约，并确认交易上链。挑战期须容纳 L1 最终性延迟、发现故障和提交挑战的时间；不能把本地 60 秒参数直接配合公网 finalized 观察使用。

若从上次 finalize 起超过 exitDelay，任何人可调用 `activateEscape()` 永久冻结。账户持有人用最终状态调用 `escapeWithdraw(state, accountIndex)`，实际充值人对 depositCursor 之后未消费的充值调用 `refundDeposit(index)`。已结算、尚未领取的提款仍可领取；其金额已从冻结状态扣除，不会双重退出。该机制不解决持续正常出批期间的交易审查。
