"""
Run a browser-use agent against a Scrapfly Browser session.

The `scrapfly` CLI provides the CDP entry point (wss://browser.scrapfly.io/...
with your API key). browser-use connects to it over CDP — Scrapfly runs the
Chromium, browser-use drives it.

Usage
-----
Prereqs:
    # one of:
    pip install browser-use
    uv pip install browser-use

Set your keys:
    export SCRAPFLY_API_KEY=scp-live-...
    export OPENAI_API_KEY=sk-...          # or ANTHROPIC_API_KEY, etc.

Run:
    python examples/browser-use-with-scrapfly.py

What happens:
    1. We shell out to `scrapfly browser ws ...` to mint a WSS URL.
       (`--pretty` prints the bare URL; otherwise parse JSON.)
    2. We hand that URL to browser-use as a CDP endpoint.
    3. browser-use's agent navigates, reasons, clicks — all on the Scrapfly-
       hosted Chromium with Scrapfly's anti-bot network in front.

Notes
-----
* The WSS URL contains your API key — don't log it.
* Use `--session <id>` on `scrapfly browser ws` to pin a stable session name
  so you can reconnect with the same cookies/state on the next run.
* For a URL that needs an anti-bot warm-up, use `scrapfly browser launch <url>`
  instead of `ws`; it returns {ws_url, session_id, run_id} after Scrapfly has
  already navigated and bypassed protection.
"""

import asyncio
import os
import subprocess
import sys

from browser_use import Agent
from browser_use.browser import BrowserProfile, BrowserSession
from browser_use.llm import ChatOpenAI


def mint_wss(*extra_args: str) -> str:
    """Call `scrapfly browser ws` and return the raw wss:// URL."""
    cmd = ["scrapfly", "browser", "ws", "--pretty", *extra_args]
    out = subprocess.run(cmd, check=True, capture_output=True, text=True)
    url = out.stdout.strip()
    if not url.startswith("wss://") and not url.startswith("ws://"):
        raise RuntimeError(f"unexpected output from scrapfly: {url!r}")
    return url


async def main() -> None:
    wss_url = mint_wss(
        "--resolution", "1920x1080",
        "--browser-brand", "chrome",
        # Pin a session so subsequent runs reuse cookies/local storage.
        # "--session", "demo",
    )

    # BrowserProfile takes any CDP endpoint — wss:// is accepted.
    session = BrowserSession(
        browser_profile=BrowserProfile(cdp_url=wss_url, is_local=False),
    )

    agent = Agent(
        task='Open https://web-scraping.dev/products and list the first 5 product names with their prices.',
        llm=ChatOpenAI(model="gpt-4.1-mini"),
        browser_session=session,
    )

    try:
        await agent.run()
    finally:
        await session.kill()


if __name__ == "__main__":
    if not os.environ.get("SCRAPFLY_API_KEY"):
        print("SCRAPFLY_API_KEY is not set", file=sys.stderr)
        sys.exit(1)
    asyncio.run(main())
