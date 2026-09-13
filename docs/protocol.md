# SettleLab 协议与存储说明

本文件定义当前实现的编码规则。所有数字使用大端定长编码，`||` 表示字节拼接。地址使用解码后的 20 字节，不使用 `0x` 文本。SHA-256 输出为 32 字节。

## 交易与签名

```text
txHash = SHA256(
  UTF8("gochain/tx/v1")
  || uint64(chainID) || uint64(amount) || uint64(nonce)
  || uint64(gasLimit) || uint64(gasPrice)
  || bytes20(from) || bytes20(to)
)
```

对 txHash 使用 secp256k1 ECDSA 签名。公钥必须为 33 字节 SEC 压缩格式；签名为规范 DER、low-S。交易 ID 不包含签名，因此同一交易内容不会因重新签名获得新 ID。调用 `Validate` 后才能将未信任输入的 `Hash` 作为合法交易身份使用。

核心 Go 类型、V0.1 Demo 与磁盘快照的 JSON 使用 uint64 数字，只在 Go 内解析。RPC 使用独立 wire 类型，把所有 uint64 编码成十进制字符串，避免 JavaScript 精度丢失。哈希为 `0x` 字符串，PublicKey / Signature 为 Base64。展示名改为 SettleLab，但保留既有哈希域。

## 状态与 Merkle

账户按小写地址字典序排序，每个账户的原始叶子：

```text
UTF8("gochain/account/v1") || bytes20(address) || uint64(balance) || uint64(nonce)
```

交易树使用交易哈希原始 32 字节作为原始叶子。两种树均使用：

```text
leafHash   = SHA256(0x00 || rawLeaf)
parentHash = SHA256(0x01 || leftHash || rightHash)
root       = SHA256(0x02 || uint64(leafCount) || treeTopHash)
```

空树的 `treeTopHash` 为 32 个零字节。奇数层复制末节点配对。叶子数量承诺用于区分 `[A,B,C]` 与 `[A,B,C,C]`。Proof 包含 Index、Total 和从叶子到根的 Siblings；验证同时检查路径长度、索引边界与奇数复制节点。

## 区块

```text
blockHash = SHA256(
  UTF8("gochain/block/v1")
  || uint64(chainID) || uint64(height) || int64(timestamp)
  || parentHash || txRoot || stateRoot
)
```

创世高度 0、时间 0、父哈希全零、交易树为空，状态来自配置分配。普通区块高度增加 1、时间严格递增，TxRoot 承诺有序交易列表。接收方从自身当前状态重执行整个交易列表，检查结果根后提交；任何失败丢弃整份工作快照。

`Chain` 和 `Pool` 的内部结构受互斥锁保护，返回区块、交易与状态时深拷贝。单个 `State` 快照本身不支持并发写；调用方也不能在传参时并发修改原始交易切片。Node 通过一把协调锁串行处理入池、快照选择、提交及查询，使这些跨容器操作保持一致。

## 持久化提交

数据目录用操作系统 advisory flock 独占，一个目录只允许一个遵守锁协议的节点打开；进程终止时操作系统释放锁，磁盘上的 LOCK 文件无需手动删除。

`chain.json` 保存 version=1、Genesis 和从高度 1 起的全部区块。每次提交：

1. 在私有状态快照上重执行并验证整块。
2. 创建同目录临时文件，写完整快照，文件 fsync 后关闭。
3. 原子 rename 替换 chain.json，然后 fsync 数据目录。
4. 成功后发布内存状态、建立回执索引并清理已消费 nonce。

写盘失败时不发布新内存状态，节点进入故障状态，拒绝查询和写入，需重启恢复。rename 已成功而目录 fsync 失败属于不确定结果，禁止盲目重试；重启后重放磁盘上实际保留的快照。提交后的客户端超时也可能已经成功，应查询交易哈希。

启动比较配置与磁盘创世块哈希，随后逐块验签、重执行并验证父哈希及 TxRoot / StateRoot。损坏记录和错误创世配置会报错，不清空历史。未完成的 `.chain-*.tmp` 文件不会被当作链数据读取。回执和状态从有效历史重建，未提交 Mempool 不恢复。

这是本地完整快照存储，写入成本随历史增长；没有增量数据库、状态 checkpoint、备份系统或对外部篡改完整有效历史前缀的额外认证。smoke 测试验证已确认提交后的进程强制退出恢复，不代表硬件断电的全面认证。

## 当前工程取舍

- 核心密码学使用 secp256k1 库，网络使用 libp2p；不实现自创密码学。
- 完整账户集合重算二叉 Merkle Tree；适合学习，暂不支持稀疏树、不存在证明和增量更新。
- 选交易时反复模拟并排序账户头部；先保证可解释、可测，后续再基于实测优化。
- 费用销毁，无出块奖励或 fee recipient；无 EVM 和任意合约调用。
- 区块全历史持久化，启动重建内存状态；暂无分叉选择、增量数据库、快照加速或区块剪枝。
- P2P 已提供身份认证、固定 authority、帧大小和并发限制，但没有 BFT、成员准入或全面抗拒绝服务能力，仍限本地实验。详见 [网络协议](p2p.md)。
