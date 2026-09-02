"""Expose the local SPNEGO HTTP proxy as a stdio MCP server for VS Code."""

from __future__ import annotations

import anyio
import signal
import threading

from mcp.client.streamable_http import streamable_http_client
from mcp.server.stdio import stdio_server
from waitress import create_server

from spnego_mcp_proxy import LISTEN_HOST, app


def stop_adapter(_signal_number: int, _frame: object) -> None:
    raise KeyboardInterrupt


async def forward_messages(source: object, destination: object) -> None:
    async for message in source:  # type: ignore[attr-defined]
        await destination.send(message)  # type: ignore[attr-defined]


async def run_adapter(local_mcp_url: str) -> None:
    async with stdio_server() as (stdio_reader, stdio_writer):
        async with streamable_http_client(local_mcp_url) as (http_reader, http_writer):
            async with anyio.create_task_group() as task_group:
                task_group.start_soon(
                    forward_messages,
                    stdio_reader,
                    http_writer,
                )
                task_group.start_soon(
                    forward_messages,
                    http_reader,
                    stdio_writer,
                )


def main() -> None:
    proxy_server = create_server(app, host=LISTEN_HOST, port=0)
    proxy_thread = threading.Thread(target=proxy_server.run, daemon=True)
    proxy_thread.start()

    try:
        local_mcp_url = (
            f"http://{proxy_server.effective_host}:"
            f"{proxy_server.effective_port}/mcp"
        )
        anyio.run(run_adapter, local_mcp_url)
    except KeyboardInterrupt:
        pass
    finally:
        proxy_server.close()


if __name__ == "__main__":
    signal.signal(signal.SIGINT, stop_adapter)
    signal.signal(signal.SIGTERM, stop_adapter)
    main()