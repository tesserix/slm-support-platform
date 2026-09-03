"""Run the Tesserix stateless MCP transport until termination."""

from __future__ import annotations

import asyncio
import signal

from .server import build_runtime


async def serve() -> None:
    runtime = build_runtime()
    stopping = asyncio.Event()
    loop = asyncio.get_running_loop()
    for received_signal in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(received_signal, stopping.set)

    await runtime.start()
    try:
        await stopping.wait()
    finally:
        await runtime.drain()
        await runtime.stop()


def main() -> None:
    asyncio.run(serve())


if __name__ == "__main__":
    main()
