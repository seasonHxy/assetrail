# 离线交易审计

## 导出与验证

交易在目标节点提交后，导出无需包含整块其他交易的证据包：

```bash
./bin/settle export --rpc http://127.0.0.1:8546 \
  --hash "$TX_HASH" --out receipt.json
```

导出会读取交易、包含证明和区块头，核对它们相互一致，再创建文件；已有文件拒绝覆盖。输出的 `claimedBlockHash` 只是服务声称的哈希，不自动获得信任。

审计方从其既有可信渠道取得 `TRUSTED_BLOCK_HASH`，然后可在不运行任何节点的机器上验证：

```bash
./bin/settle verify --file receipt.json --block-hash "$TRUSTED_BLOCK_HASH"
```

成功返回 `valid:true`、`scope:"signed-transaction-inclusion"`，以及交易哈希、链 ID、付款方、收款方、金额、nonce 和区块位置。错误时退出码为 1，不输出成功结果。该命令没有 RPC 参数，验证包 `audit` 也不依赖网络模块。

## 到底证明什么

验证器执行：

1. 检查格式版本、区块高度/时间和允许的交易数量。
2. 从区块头重新计算区块哈希，要求与外部提供的可信哈希一致。
3. 验证交易的规范签名、公钥与付款地址、链 ID 和数值约束。
4. 从交易原始字段重算交易哈希。
5. 核对带叶子总数与位置的 Merkle 路径，要求重算出的根等于已固定区块头中的 TxRoot。

这证明特定付款地址授权了该交易，以及它属于一个已被审计方认可的区块。它不独立证明签名者真实身份、足够余额、所有状态变更的正确性、链的最终性或其他交易隐私，也不是 ZK。

## 信任锚为何不能省略

任何人都可以生成自己的密钥、交易和自洽的 Merkle Tree。如果验证器直接信任包内的根，“验证通过”只能说明这些字段自洽。

因此 `--block-hash` 必填，且拒绝全零锚。调用者应使用已经确认来源的检查点，例如自己此前验证保存的区块哈希。当前网络基于固定 authority；从另一副本读取哈希仍依赖同一 authority，不能称为独立共识验证。

`audit` 测试包含自洽伪造树的反例：它在原有可信锚下失败；如果调用者明确改为信任其新根，则会通过。这展示了信任输入的边界，而不是试图隐藏它。

## 格式与兼容性

- 版本为 `settlelab.audit.v1`，包含一笔 `transaction`、`header` 和 `proof`。
- 金额、Gas、nonce、高度、时间和证明索引均为十进制 JSON 字符串，避免 JavaScript 大整数舍入。
- 哈希为 `0x` 十六进制字符串；压缩公钥和 DER 签名为 Base64。
- 解码限制为 16 KiB，拒绝未知字段和尾随 JSON 文档；没有私钥或整块交易列表。
- 不修改 V0.1 交易及区块哈希域，现有历史仍可生成证据。

## 离线签名

负载实验和外部客户端也可先准备签名交易，再提交：

```bash
./bin/settle sign --key .settlelab/alice.key --to "$BOB" \
  --amount 1 --nonce 0 --chain-id 9001
```

此命令不查询 RPC，必须显式提供 nonce 和 Chain ID。输出与 `chain_sendRawTransaction` 相容的原始编码，可用于客户端自行管理 nonce 的流程。

## 复现实验

`make evidence` 生成报告和证据包，包含在所有节点停止之后的验证、修改金额后的失败，以及错误信任锚的失败。见 [项目价值与实测](value.md)。
