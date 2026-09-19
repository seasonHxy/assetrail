// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.28;

interface IERC20Rail {
    function balanceOf(address) external view returns (uint256);
    function transfer(address, uint256) external returns (bool);
    function transferFrom(address, address, uint256) external returns (bool);
}

/// @notice Bounded application-specific optimistic rollup, NOT a general EVM rollup.
/// Safety needs an honest challenger before the window expires and available L1.
/// All input data stays on L1. Static admission is not a legal compliance system.
contract AssetRail {
    uint256 public constant OP_BYTES = 173;
    uint256 public constant MAX_OPS = 32;
    uint256 private constant HALF_N = 0x7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0;

    struct State {
        uint64[8] cash;
        uint64[8] rwa;
        uint64[8] nonce;
    }

    struct Deposit {
        address sender;
        uint8 account;
        uint8 asset;
        uint64 amount;
    }

    struct Batch {
        bytes data;
        bytes32 preRoot;
        bytes32 postRoot;
        uint256 start;
        uint256 end;
        uint64 sequence;
        uint256 deadline;
        address proposer;
        uint8 status; // 1 pending, 2 finalized, 3 rejected/cancelled
    }
    address[] public accounts;
    IERC20Rail[2] public tokens; // 0 payment, 1 RWA
    uint256 public immutable challengePeriod;
    uint256 public immutable exitDelay;
    uint256 public immutable bond;
    bytes32 public stateRoot;
    uint64 public sequence;
    uint256 public depositCursor;
    Deposit[] public deposits;
    mapping(uint256 => Batch) private batches;
    mapping(bytes32 => bool) public claimed;
    mapping(uint256 => bool) public refunded;
    mapping(address => uint256) public credits;
    mapping(uint8 => bool) public exited;
    uint256 public nextBatchId;
    uint256 public pendingId;
    uint256 public lastFinalizedAt;
    bool public escaped;
    bool private entered;

    event Deposited(uint256 indexed index, address indexed sender, uint8 account, uint8 asset, uint64 amount);
    event Proposed(
        uint256 indexed id, uint64 sequence, bytes32 preRoot, bytes32 postRoot, uint256 start, uint256 end, bytes data
    );
    event Finalized(uint256 indexed id, bytes32 root);
    event Rejected(uint256 indexed id, address challenger);
    event Withdrawal(uint256 indexed id, uint256 index, address recipient, uint8 asset, uint64 amount);
    event Escape(bytes32 root);
    modifier nonReentrant() {
        require(!entered, "reentrant");
        entered = true;
        _;
        entered = false;
    }

    constructor(
        address cashToken,
        address rwaToken,
        address[] memory members,
        uint256 window,
        uint256 delay,
        uint256 proposerBond
    ) {
        require(cashToken.code.length > 0 && rwaToken.code.length > 0 && cashToken != rwaToken, "tokens");
        require(
            members.length > 0 && members.length <= 8 && window > 0 && delay >= window * 2 && proposerBond > 0, "config"
        );
        for (uint256 i; i < members.length; i++) {
            require(members[i] != address(0), "zero member");
            for (uint256 j; j < i; j++) {
                require(members[i] != members[j], "duplicate member");
            }
            accounts.push(members[i]);
        }
        tokens[0] = IERC20Rail(cashToken);
        tokens[1] = IERC20Rail(rwaToken);
        challengePeriod = window;
        exitDelay = delay;
        bond = proposerBond;
        State memory initial;
        stateRoot = hashState(initial);
        lastFinalizedAt = block.timestamp;
    }

    function hashState(State memory s) public pure returns (bytes32) {
        return keccak256(abi.encode(s));
    }

    function accountCount() external view returns (uint256) {
        return accounts.length;
    }

    function depositCount() external view returns (uint256) {
        return deposits.length;
    }

    function getBatch(uint256 id) external view returns (Batch memory) {
        return batches[id];
    }

    function batchData(uint256 id) external view returns (bytes memory) {
        return batches[id].data;
    }

    function batchInfo(uint256 id)
        external
        view
        returns (bytes32, bytes32, uint256, uint256, uint64, uint256, address, uint8)
    {
        Batch storage b = batches[id];
        return (b.preRoot, b.postRoot, b.start, b.end, b.sequence, b.deadline, b.proposer, b.status);
    }

    function deposit(uint8 account, uint8 asset, uint64 amount) external nonReentrant {
        require(!escaped && account < accounts.length && asset < 2 && amount > 0, "deposit");
        uint256 beforeBalance = tokens[asset].balanceOf(address(this));
        require(tokens[asset].transferFrom(msg.sender, address(this), amount), "transferFrom");
        require(tokens[asset].balanceOf(address(this)) == beforeBalance + amount, "unsupported token");
        deposits.push(Deposit(msg.sender, account, asset, amount));
        emit Deposited(deposits.length - 1, msg.sender, account, asset, amount);
    }

    function propose(bytes calldata data, uint256 end, bytes32 postRoot) external payable returns (uint256 id) {
        require(!escaped && pendingId == 0 && msg.value == bond, "proposal unavailable");
        require(end >= depositCursor && end <= deposits.length && end - depositCursor <= MAX_OPS, "deposit range");
        require(data.length % OP_BYTES == 0 && data.length / OP_BYTES <= MAX_OPS, "batch size");
        require(data.length > 0 || end > depositCursor, "empty batch");
        // Syntax checked up front so even a dishonest finalized batch cannot make
        // claim decoding unbounded or access arbitrary recipient/token addresses.
        for (uint256 i; i < data.length; i += OP_BYTES) {
            require(
                uint8(data[i]) <= 4 && uint8(data[i + 1]) < accounts.length && uint8(data[i + 2]) < accounts.length,
                "operation"
            );
        }
        id = ++nextBatchId;
        batches[id] = Batch(
            data,
            stateRoot,
            postRoot,
            depositCursor,
            end,
            sequence + 1,
            block.timestamp + challengePeriod,
            msg.sender,
            1
        );
        pendingId = id;
        emit Proposed(id, sequence + 1, stateRoot, postRoot, depositCursor, end, data);
    }

    function challenge(State calldata previous) external {
        uint256 id = pendingId;
        Batch storage b = batches[id];
        require(id != 0 && b.status == 1 && block.timestamp < b.deadline, "challenge closed");
        require(hashState(previous) == b.preRoot, "wrong witness");
        bool fraud;
        try this.evaluate(previous, b.data, b.start, b.end, b.sequence) returns (State memory result) {
            fraud = hashState(result) != b.postRoot;
        } catch (bytes memory reason) {
            // Out-of-gas must not turn a valid batch into a successful challenge.
            require(reason.length >= 4, "verification incomplete");
            fraud = true;
        }
        require(fraud, "valid batch");
        b.status = 3;
        pendingId = 0;
        credits[msg.sender] += bond;
        emit Rejected(id, msg.sender);
    }

    function finalize() external {
        uint256 id = pendingId;
        Batch storage b = batches[id];
        require(!escaped && id != 0 && b.status == 1 && block.timestamp >= b.deadline, "not finalizable");
        stateRoot = b.postRoot;
        depositCursor = b.end;
        sequence = b.sequence;
        b.status = 2;
        pendingId = 0;
        lastFinalizedAt = block.timestamp;
        credits[b.proposer] += bond;
        emit Finalized(id, stateRoot);
    }

    function claimWithdrawal(uint256 id, uint256 index) external nonReentrant {
        Batch storage b = batches[id];
        require(b.status == 2 && index < b.data.length / OP_BYTES, "not finalized");
        bytes memory op = b.data;
        uint256 p = index * OP_BYTES;
        uint8 kind = uint8(op[p]);
        require(kind == 3 || kind == 4, "not withdrawal");
        bytes32 key = keccak256(abi.encode(id, index));
        require(!claimed[key], "already claimed");
        claimed[key] = true;
        uint8 asset = kind == 3 ? 0 : 1;
        address recipient = accounts[uint8(op[p + 1])];
        uint64 amount = readMemory64(op, p + 3);
        require(tokens[asset].transfer(recipient, amount), "transfer");
        emit Withdrawal(id, index, recipient, asset, amount);
    }

    function withdrawCredit() external nonReentrant {
        uint256 amount = credits[msg.sender];
        require(amount > 0, "no credit");
        credits[msg.sender] = 0;
        (bool ok,) = msg.sender.call{value: amount}("");
        require(ok, "credit transfer");
    }

    // Permanently freeze to the last finalized root if progress stops. A pending
    // proposal cannot extend this deadline. Deposits not in that root are refunded.
    function activateEscape() external {
        require(!escaped && block.timestamp >= lastFinalizedAt + exitDelay, "escape not ready");
        escaped = true;
        if (pendingId != 0) {
            Batch storage b = batches[pendingId];
            b.status = 3;
            credits[b.proposer] += bond;
            pendingId = 0;
        }
        emit Escape(stateRoot);
    }

    function escapeWithdraw(State calldata previous, uint8 account) external nonReentrant {
        require(escaped && account < accounts.length && msg.sender == accounts[account], "escape account");
        require(!exited[account] && hashState(previous) == stateRoot, "escape witness");
        exited[account] = true;
        if (previous.cash[account] > 0) {
            require(tokens[0].transfer(msg.sender, previous.cash[account]), "cash transfer");
        }
        if (previous.rwa[account] > 0) require(tokens[1].transfer(msg.sender, previous.rwa[account]), "rwa transfer");
    }

    function refundDeposit(uint256 index) external nonReentrant {
        require(escaped && index >= depositCursor && index < deposits.length && !refunded[index], "refund");
        Deposit memory d = deposits[index];
        require(msg.sender == d.sender, "depositor");
        refunded[index] = true;
        require(tokens[d.asset].transfer(msg.sender, d.amount), "refund transfer");
    }

    function digest(bytes calldata payload) public view returns (bytes32) {
        require(payload.length == 43, "payload");
        return keccak256(abi.encodePacked("assetrail/payment/v1", uint256(block.chainid), address(this), payload));
    }

    function signer(bytes32 h, bytes calldata sig) private pure returns (address who) {
        require(sig.length == 65, "signature length");
        bytes32 r;
        bytes32 s;
        uint8 v;
        assembly ("memory-safe") {
            r := calldataload(sig.offset)
            s := calldataload(add(sig.offset, 32))
            v := byte(0, calldataload(add(sig.offset, 64)))
        }
        require(uint256(s) > 0 && uint256(s) <= HALF_N && (v == 27 || v == 28), "canonical signature");
        who = ecrecover(h, v, r, s);
        require(who != address(0), "signature");
    }

    function read64(bytes calldata data, uint256 p) private pure returns (uint64 x) {
        assembly ("memory-safe") { x := shr(192, calldataload(add(data.offset, p))) }
    }

    function readMemory64(bytes memory data, uint256 p) private pure returns (uint64 x) {
        assembly ("memory-safe") { x := shr(192, mload(add(add(data, 32), p))) }
    }

    function evaluate(State memory s, bytes calldata data, uint256 start, uint256 end, uint64 seq)
        external
        view
        returns (State memory)
    {
        require(
            end >= start && end <= deposits.length && end - start <= MAX_OPS && data.length % OP_BYTES == 0
                && data.length / OP_BYTES <= MAX_OPS,
            "input"
        );
        for (uint256 i = start; i < end; i++) {
            Deposit memory d = deposits[i];
            if (d.asset == 0) s.cash[d.account] += d.amount;
            else s.rwa[d.account] += d.amount;
        }
        for (uint256 p; p < data.length; p += OP_BYTES) {
            uint8 kind = uint8(data[p]);
            uint8 from = uint8(data[p + 1]);
            uint8 to = uint8(data[p + 2]);
            uint64 amount = read64(data, p + 3);
            uint64 price = read64(data, p + 11);
            uint64 nonce = read64(data, p + 19);
            uint64 counterNonce = read64(data, p + 27);
            require(kind <= 4 && from < accounts.length && to < accounts.length && amount > 0, "operation");
            require(read64(data, p + 35) == seq && nonce == s.nonce[from], "sequence/nonce");
            bytes32 h = digest(data[p:p + 43]);
            require(signer(h, data[p + 43:p + 108]) == accounts[from], "sender");
            s.nonce[from]++;
            if (kind == 2) {
                require(from != to && price > 0 && counterNonce == s.nonce[to], "DvP");
                require(signer(h, data[p + 108:p + 173]) == accounts[to], "counterparty");
                s.nonce[to]++;
                s.rwa[from] -= amount;
                s.rwa[to] += amount;
                s.cash[to] -= price;
                s.cash[from] += price;
            } else {
                require(
                    price == 0 && counterNonce == 0 && keccak256(data[p + 108:p + 173]) == keccak256(new bytes(65)),
                    "unused fields"
                );
                if (kind >= 3) require(to == from, "withdraw recipient");
                if (kind == 0) {
                    s.cash[from] -= amount;
                    s.cash[to] += amount;
                }
                if (kind == 1) {
                    s.rwa[from] -= amount;
                    s.rwa[to] += amount;
                }
                if (kind == 3) s.cash[from] -= amount;
                if (kind == 4) s.rwa[from] -= amount;
            }
        }
        return s;
    }
}
