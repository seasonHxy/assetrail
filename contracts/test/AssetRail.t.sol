// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.28;
import "../src/AssetRail.sol";
import "../src/MockAsset.sol";

interface Vm {
    function addr(uint256) external returns (address);
    function sign(uint256, bytes32) external returns (uint8, bytes32, bytes32);
    function prank(address) external;
    function deal(address, uint256) external;
    function warp(uint256) external;
    function expectRevert() external;
}

contract AssetRailTest {
    Vm constant vm = Vm(address(uint160(uint256(keccak256("hevm cheat code")))));
    AssetRail rail;
    MockAsset cash;
    MockAsset rwa;
    address alice;
    address bob;
    address outsider;

    function setUp() public {
        alice = vm.addr(1);
        bob = vm.addr(2);
        outsider = vm.addr(3);
        cash = new MockAsset("Test Payment", "tPAY", false);
        rwa = new MockAsset("Test RWA", "tRWA", true);
        address[] memory members = new address[](2);
        members[0] = alice;
        members[1] = bob;
        rail = new AssetRail(address(cash), address(rwa), members, 60, 300, 1 ether);
        rwa.admit(alice);
        rwa.admit(bob);
        rwa.admit(address(rail));
        cash.mint(bob, 1000);
        rwa.mint(alice, 100);
        vm.prank(bob);
        cash.approve(address(rail), 1000);
        vm.prank(bob);
        rail.deposit(1, 0, 1000);
        vm.prank(alice);
        rwa.approve(address(rail), 100);
        vm.prank(alice);
        rail.deposit(0, 1, 100);
        vm.deal(address(this), 100 ether);
    }

    function signature(uint256 key, bytes memory payload) internal returns (bytes memory) {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, rail.digest(payload));
        return abi.encodePacked(r, s, v);
    }

    function op(uint8 kind, uint8 from, uint8 to, uint64 amount, uint64 price, uint64 nonce, uint64 counter, uint64 seq)
        internal
        returns (bytes memory)
    {
        bytes memory payload = abi.encodePacked(kind, from, to, amount, price, nonce, counter, seq);
        bytes memory b = kind == 2 ? signature(uint256(to) + 1, payload) : new bytes(65);
        return abi.encodePacked(payload, signature(uint256(from) + 1, payload), b);
    }
    function empty() internal pure returns (AssetRail.State memory s) {}

    function depositState() internal pure returns (AssetRail.State memory s) {
        s.cash[1] = 1000;
        s.rwa[0] = 100;
    }

    function traded() internal pure returns (AssetRail.State memory s) {
        s.cash[0] = 250;
        s.cash[1] = 750;
        s.rwa[0] = 90;
        s.rwa[1] = 10;
        s.nonce[0] = 1;
        s.nonce[1] = 1;
    }

    function trade() internal returns (bytes memory) {
        return op(2, 0, 1, 10, 250, 0, 0, 1);
    }

    function finish() internal {
        vm.warp(block.timestamp + 60);
        rail.finalize();
    }

    function testDepositDvPAndFinalizedWithdrawal() public {
        AssetRail.State memory s = traded();
        bytes memory data = trade();
        AssetRail.State memory evaluated = rail.evaluate(empty(), data, 0, 2, 1);
        require(rail.hashState(evaluated) == rail.hashState(s), "execution");
        rail.propose{value: 1 ether}(data, 2, rail.hashState(s));
        vm.expectRevert();
        rail.finalize();
        finish();
        require(rail.stateRoot() == rail.hashState(s) && rail.depositCursor() == 2, "final state");
        bytes memory withdrawal = op(4, 1, 1, 3, 0, 1, 0, 2);
        s.rwa[1] -= 3;
        s.nonce[1]++;
        uint256 id = rail.propose{value: 1 ether}(withdrawal, 2, rail.hashState(s));
        vm.expectRevert();
        rail.claimWithdrawal(id, 0);
        finish();
        vm.prank(outsider);
        rail.claimWithdrawal(id, 0);
        require(rwa.balanceOf(bob) == 3 && rwa.balanceOf(outsider) == 0, "recipient");
        vm.expectRevert();
        rail.claimWithdrawal(id, 0);
    }

    function testFalseRootRejectedAndHonestRecovery() public {
        uint256 id = rail.propose{value: 1 ether}(trade(), 2, bytes32(uint256(123)));
        vm.prank(outsider);
        rail.challenge(empty());
        require(rail.pendingId() == 0 && rail.stateRoot() == rail.hashState(empty()), "rejection");
        require(rail.credits(outsider) == 1 ether && rail.getBatch(id).status == 3, "bond");
        rail.propose{value: 1 ether}(trade(), 2, rail.hashState(traded()));
        finish();
        require(rail.stateRoot() == rail.hashState(traded()), "recovery");
    }

    function testInvalidSignatureAndInsufficientCashAreFraud() public {
        bytes memory data = trade();
        data[45] = bytes1(uint8(data[45]) ^ 1);
        rail.propose{value: 1 ether}(data, 2, rail.hashState(traded()));
        rail.challenge(empty());
        bytes memory expensive = op(2, 0, 1, 10, 1001, 0, 0, 1);
        rail.propose{value: 1 ether}(expensive, 2, rail.hashState(traded()));
        rail.challenge(empty());
        require(rail.pendingId() == 0 && rail.depositCursor() == 0, "invalid execution advanced deposits");
    }

    function testHonestBatchCannotBeChallengedWithWrongWitnessOrLowGas() public {
        uint256 id = rail.propose{value: 1 ether}(trade(), 2, rail.hashState(traded()));
        vm.expectRevert();
        rail.challenge(depositState());
        vm.expectRevert();
        rail.challenge(empty());
        for (uint256 g = 30000; g <= 300000; g += 30000) {
            (bool ok,) = address(rail).call{gas: g}(abi.encodeCall(rail.challenge, (empty())));
            require(!ok, "gas griefing");
        }
        require(rail.pendingId() == id && rail.getBatch(id).status == 1, "valid batch rejected");
    }

    function testReplayAndCrossDomainFail() public {
        bytes memory data = trade();
        rail.propose{value: 1 ether}(data, 2, rail.hashState(traded()));
        finish();
        rail.propose{value: 1 ether}(data, 2, rail.hashState(traded()));
        rail.challenge(traded());
        address[] memory members = new address[](2);
        members[0] = alice;
        members[1] = bob;
        AssetRail other = new AssetRail(address(cash), address(rwa), members, 60, 300, 1 ether);
        vm.expectRevert();
        other.evaluate(depositState(), data, 0, 0, 1);
    }

    function testEscapePendingCannotBlockAndUnconsumedDepositRefund() public {
        rail.propose{value: 1 ether}("", 2, rail.hashState(depositState()));
        finish();
        cash.mint(bob, 50);
        vm.prank(bob);
        cash.approve(address(rail), 50);
        vm.prank(bob);
        rail.deposit(1, 0, 50);
        bytes memory data = op(0, 1, 0, 10, 0, 0, 0, 2);
        rail.propose{value: 1 ether}(data, 3, bytes32(uint256(999)));
        vm.warp(block.timestamp + 300);
        rail.activateEscape();
        require(rail.pendingId() == 0 && rail.escaped(), "escape");
        vm.prank(bob);
        rail.escapeWithdraw(depositState(), 1);
        require(cash.balanceOf(bob) == 1000, "exit balance");
        vm.expectRevert();
        vm.prank(bob);
        rail.escapeWithdraw(depositState(), 1);
        vm.prank(bob);
        rail.refundDeposit(2);
        require(cash.balanceOf(bob) == 1050, "deposit refund");
        vm.expectRevert();
        vm.prank(bob);
        rail.refundDeposit(2);
        vm.expectRevert();
        rail.propose{value: 1 ether}(data, 3, bytes32(uint256(999)));
    }

    function testLateChallengeAndInvalidIngress() public {
        vm.expectRevert();
        rail.deposit(7, 0, 1);
        vm.expectRevert();
        rail.propose{value: 1 ether}(hex"01", 2, bytes32(0));
        vm.expectRevert();
        rail.propose{value: 1 ether}("", 3, bytes32(0));
        rail.propose{value: 1 ether}(trade(), 2, rail.hashState(traded()));
        vm.warp(block.timestamp + 60);
        vm.expectRevert();
        rail.challenge(empty());
        rail.finalize();
    }

    function testMaxBatchValidChallengeDoesNotRunOutOfGas() public {
        AssetRail.State memory s = depositState();
        bytes memory data;
        for (uint64 i; i < 32; i++) {
            data = bytes.concat(data, op(0, 1, 1, 1, 0, i, 0, 1));
            s.nonce[1]++;
        }
        rail.propose{value: 1 ether}(data, 2, rail.hashState(s));
        vm.expectRevert();
        rail.challenge(empty());
        require(rail.pendingId() != 0, "valid maximal batch rejected");
    }

    function testAllOperationsAndNonCanonicalUnusedFields() public {
        bytes memory data = bytes.concat(
            op(0, 1, 0, 20, 0, 0, 0, 1),
            op(1, 0, 1, 5, 0, 0, 0, 1),
            op(3, 0, 0, 10, 0, 1, 0, 1),
            op(4, 1, 1, 2, 0, 1, 0, 1)
        );
        AssetRail.State memory s = rail.evaluate(empty(), data, 0, 2, 1);
        require(s.cash[0] == 10 && s.cash[1] == 980 && s.rwa[0] == 95 && s.rwa[1] == 3, "all operation semantics");
        bytes memory bad = op(0, 1, 0, 1, 0, 0, 0, 1);
        bad[172] = 0x01;
        rail.propose{value: 1 ether}(bad, 2, rail.hashState(s));
        rail.challenge(empty());
        require(rail.pendingId() == 0, "noncanonical unused signature");
    }

    function testFuzzDvPConservesBothAssets(uint64 amount, uint64 price) public {
        amount = uint64(uint256(amount) % 100 + 1);
        price = uint64(uint256(price) % 1000 + 1);
        AssetRail.State memory s = rail.evaluate(empty(), op(2, 0, 1, amount, price, 0, 0, 1), 0, 2, 1);
        require(s.cash[0] + s.cash[1] == 1000 && s.rwa[0] + s.rwa[1] == 100, "conservation");
    }
}
