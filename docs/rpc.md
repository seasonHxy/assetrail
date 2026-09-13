# SettleLab JSON-RPC

默认地址：`http://127.0.0.1:8545/`。使用 POST，Content-Type 为 `application/json`。所有方法只接受下表列出的参数，除等待接口只接受对象外，可以使用位置数组或对应名称的对象；无参方法接受省略 params、`[]` 或 `{}`。

```json
{"jsonrpc":"2.0","id":1,"method":"chain_getBlock","params":["latest"]}
```

| 方法 | 位置参数 / 名称 | 结果 |
| --- | --- | --- |
| `chain_getChainId` | 无 | Chain ID 字符串 |
| `chain_getBlockNumber` | 无 | 当前高度字符串 |
| `chain_getStatus` | 无 | 名称、版本、运行模式、高度、区块哈希、StateRoot、pending/queued |
| `chain_getPeers` | 无 | 网络启用状态、Peer ID、监听地址、连接和最近错误；BFT 另带 consensus 状态 |
| `chain_getMempool` | 无 | pending / queued 数量字符串 |
| `chain_getBalance` | `[address]` / `address` | 余额字符串 |
| `chain_getNonce` | `[address]` / `address` | 已确认的下一笔 nonce 字符串 |
| `chain_getPendingNonce` | `[address]` / `address` | 从已确认 nonce 开始，跳过本节点连续待处理 nonce 后的第一个空位 |
| `chain_getAccount` | `[address]` / `address` | 地址、余额、nonce |
| `chain_getBlock` | `[height]` / `height` | 区块详情；支持 `"latest"`，不存在返回 null |
| `chain_getBlockHeader` | `[height]` / `height` | 仅返回区块哈希和区块头，用于轻量证据导出；不存在返回 null |
| `chain_getCommitCertificate` | `[height]` / `height` | 实验共识节点的提交证书，支持 `"latest"`；旧模式或无证书返回 null |
| `chain_sendRawTransaction` | `[raw]` / `raw` | 入池成功后返回交易哈希 |
| `chain_getTransaction` | `[hash]` / `hash` | 交易及 pending/committed 状态；不存在返回 null |
| `chain_getTransactionReceipt` | `[hash]` / `hash` | 已持久化回执；未确认或不存在返回 null |
| `chain_waitForTransactionReceipt` | 仅对象 `{hash, timeoutMs?}` | 持久化后返回回执；本次等待到期返回 null |
| `chain_getTransactionProof` | `[hash]` / `hash` | 所在块信息、TxRoot、包含证明；未确认或不存在返回 null |

例如：

```bash
curl http://127.0.0.1:8545/ \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"chain_getBlock","params":{"height":"latest"}}'
```

所有链上无符号数值用规范十进制字符串表示，拒绝 `"01"`、`"+1"`、`"1e3"` 和 JSON number。JSON-RPC 的 id 与 error.code 遵循标准，可使用数字；id 原样保留，不经过浮点解析。

## 签名交易

使用 `settle transfer` 或 Go 中的 `rpc.EncodeRawTransaction`。`raw` 为 `0x + hex(UTF8 JSON)`，内层包含：

```json
{
  "chainId":"9001",
  "from":"<小写 0x 地址>",
  "to":"<小写 0x 地址>",
  "amount":"20",
  "nonce":"0",
  "gasLimit":"21000",
  "gasPrice":"0",
  "publicKey":"<33 字节压缩公钥的 Base64>",
  "signature":"<DER 签名的 Base64>"
}
```

签名对象仍是协议文件定义的固定二进制字段哈希，不是 JSON 文本。交易在客户端签名，服务端只接收公钥和签名，不接收私钥。与 Ethereum 的 `eth_sendRawTransaction` 格式不同。

提交返回仅表示入池，余额不足或 nonce 有间隔的交易可能继续等待。使用回执判断是否已经提交。`settle transfer` 默认调用 `chain_getPendingNonce`，支持向同一节点顺序连续提交。并发客户端、跨节点提交仍需自行协调 nonce；此查询不预留 nonce，也不保证余额足够。替换交易仍使用原 nonce。

## 回执与证明

回执包含 `transactionHash`、`blockHash`、`blockHeight`、`transactionIndex`、`gasUsed`、`fee`、`status:"committed"`、`confirmationScope:"local-durable"`。本链不收录失败执行的交易，因而没有链上失败回执；无效交易在入池或候选执行阶段被拒绝/跳过。follower 同步到对应区块后才返回回执；此时表示本地已经验证并持久化，不代表 BFT finality。

`chain_getStatus.mode` 可为 `single-node`、`authority`、`follower`、`bft-experimental`。BFT 的 `peerId` 是验证者公钥十六进制标识，`chain_getPeers.id` 是对应 libp2p Peer ID。authority/follower 模式附带 `authority`，其 `chain_getPeers` 的 `connectedCount` 使用十进制字符串；每个 peer 包含 `connected`、`lastSuccess`、`lastError`。solo 模式返回 `enabled:false`。网络连接数量与已追到的区块高度是不同指标。

证明的 `root` 是对应历史区块的 TxRoot，叶子原始值是交易哈希的 32 字节。`proof.index` 和 `proof.total` 是十进制字符串，`proof.siblings` 是哈希数组（单叶树可能为 null，等价于空路径）。客户端转换后使用 `crypto.VerifyProof` 验证，并根据其信任模型核对区块根。

CLI 已提供 `settle export` 将交易、区块头和证明整理为证据包，使用 `settle verify --file ... --block-hash ...` 离线验真。必须额外提供可信锚；不要把一个未知服务自行返回的哈希直接视为可信检查点。详见 [离线审计](audit.md)。

## HTTP 和协议行为

- 每个 HTTP 请求最大 1 MiB，每批最多 32 个调用；超限批次整体拒绝，不执行其中请求。
- 批次顺序执行，响应只包含带 id 的请求。合法通知不回复，纯通知请求返回 HTTP 204；提交交易建议带 id 才能获知错误。
- 解析错误 `-32700`，非法请求 `-32600`，未知方法 `-32601`，参数/原始编码错误 `-32602`，业务拒绝和节点不可用 `-32000`。
- 无效签名/错链/重复交易拒绝。有效格式的签名交易可能因 nonce、替换价格或池容量在入池时被拒绝。
- 非 POST、错误 Content-Type、过大的 HTTP body 返回对应 HTTP 错误。未知路径 404。
- 本版本没有浏览器客户端，拒绝 Origin 请求；没有 RPC 认证和多租户隔离，默认仅绑定 loopback。
- `/healthz` 可用时返回 200；节点已关闭或写盘失败后返回 503。

接口基于 [JSON-RPC 2.0 官方规范](https://www.jsonrpc.org/specification)。

## 等待确认

```json
{"jsonrpc":"2.0","id":1,"method":"chain_waitForTransactionReceipt","params":{"hash":"<交易哈希>","timeoutMs":"5000"}}
```

`timeoutMs` 为 1..5000 的十进制字符串，省略时为 5000。已有回执立即返回；其余请求等待本节点持久化提交事件，避免反复扫描交易或复制区块。单个 RPC Server 最多容纳 128 个等待请求，超出返回 `-32005`；已有回执查询不占等待槽。客户端断开、节点关闭或写盘失败会结束等待并释放槽位。

等待到期的 null 只表示本次没有观察到确认，交易可能仍在池中、未传播到该节点或已被替换。该方法拒绝出现在批次中（`-32600`），通知调用立即返回 HTTP 204 且不建立等待。普通回执批量查询继续可用。`settle wait` 会按 5 秒窗口续等，直到用户指定的整体超时；`settle transfer --wait` 复用同一逻辑。详见 [确认说明](confirmation.md)。

使用 `settlelab-node --validators ... --p2p-key ...` 启用实验 BFT 后，回执 scope 为 `certificate-verified-local`，提交证书包含 3..4 个不同验证者的预提交签名。

BFT 的 `chain_getPeers.consensus` 返回：`validator`、`setId`、正在投票的 `height`（账本高度加一）、`round`、`phase`（propose/prevote/precommit）、`proposer`、常量 `quorum:3`、当前阶段 `deadline`，以及本次进程的 `timeouts`、`roundCatchups`、`rejectedMessages` 计数。高度、轮次和计数为十进制字符串，deadline 为 RFC3339 时间；拒绝计数不等于恶意节点数。连接成功或持续换轮均不等于已有提交证书。详见 [共识网络说明](consensus.md)。
