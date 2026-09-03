from mcp_gateway.server import ToolRegistry


def test_mutating_tools_are_denied_by_default() -> None:
    registry = ToolRegistry()

    @registry.tool(name="write", mutating=True)
    async def write() -> None:
        pass

    assert [tool["name"] for tool in registry.list_tools()] == []


def test_mutating_tools_require_explicit_opt_in() -> None:
    registry = ToolRegistry(allow_mutations=True)

    @registry.tool(name="write", mutating=True)
    async def write() -> None:
        pass

    assert [tool["name"] for tool in registry.list_tools()] == ["write"]


def test_non_mutating_tools_remain_available() -> None:
    registry = ToolRegistry()

    @registry.tool(name="read")
    async def read() -> None:
        pass

    assert [tool["name"] for tool in registry.list_tools()] == ["read"]
