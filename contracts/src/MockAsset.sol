// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.28;

/// Test assets only: no real-world backing, yield, redemption or identity provider.
contract MockAsset {
    string public name;
    string public symbol;
    uint8 public constant decimals = 6;
    address public immutable issuer;
    bool public immutable restricted;
    mapping(address => bool) public allowed;
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;
    event Transfer(address indexed from, address indexed to, uint256 value);
    event Approval(address indexed owner, address indexed spender, uint256 value);

    constructor(string memory n, string memory s, bool r) {
        name = n;
        symbol = s;
        restricted = r;
        issuer = msg.sender;
    }

    function admit(address who) external {
        require(msg.sender == issuer, "issuer");
        allowed[who] = true;
    }

    function mint(address to, uint256 amount) external {
        require(msg.sender == issuer && (!restricted || allowed[to]), "mint");
        balanceOf[to] += amount;
        emit Transfer(address(0), to, amount);
    }

    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
        emit Approval(msg.sender, spender, amount);
        return true;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        move(msg.sender, to, amount);
        return true;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        allowance[from][msg.sender] -= amount;
        move(from, to, amount);
        return true;
    }

    function move(address from, address to, uint256 amount) private {
        require(to != address(0) && (!restricted || (allowed[from] && allowed[to])), "admission");
        balanceOf[from] -= amount;
        balanceOf[to] += amount;
        emit Transfer(from, to, amount);
    }
}
