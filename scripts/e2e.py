#!/usr/bin/env python3
"""
E2E test runner for clc-core API.

Prompts for params, shows payload, runs the request, shows the response.

Add new endpoints to the ENDPOINTS list at the bottom of this file.

Requirements:
    pip install requests PyJWT[cryptography] tomli
    (tomli not needed on Python 3.11+)
"""

import json
import re
import sys
import time
from pathlib import Path
from typing import Any

# ── stdlib shims ──────────────────────────────────────────────────────────────

try:
    import tomllib
except ImportError:
    try:
        import tomli as tomllib  # type: ignore[no-redef]
    except ImportError:
        print("pip install tomli   (or use Python 3.11+)")
        sys.exit(1)

try:
    import requests
except ImportError:
    print("pip install requests")
    sys.exit(1)

try:
    import jwt
    from cryptography.hazmat.primitives.serialization import load_pem_private_key
except ImportError:
    print("pip install PyJWT[cryptography]")
    sys.exit(1)

# ── ANSI colours ──────────────────────────────────────────────────────────────

R = "\033[0m"
BOLD = "\033[1m"
DIM = "\033[2m"
GREEN = "\033[32m"
CYAN = "\033[36m"
YELLOW = "\033[33m"
RED = "\033[31m"
MAGENTA = "\033[35m"


def col(text: Any, *codes: str) -> str:
    return "".join(codes) + str(text) + R


def hr(char: str = "─", width: int = 64) -> None:
    print(col(char * width, DIM))


# ── Config ────────────────────────────────────────────────────────────────────

ROOT = Path(__file__).parent.parent
CONFIG_PATH = ROOT / "config.toml"


def load_config() -> dict:
    try:
        with open(CONFIG_PATH, "rb") as f:
            return tomllib.load(f)
    except FileNotFoundError:
        print(col(f"config.toml not found at {CONFIG_PATH}", RED))
        sys.exit(1)


def base_url(cfg: dict) -> str:
    addr = cfg["api"]["address"]
    if addr.startswith(":"):
        addr = f"localhost{addr}"
    scheme = "https" if cfg["api"].get("prod") else "http"
    return f"{scheme}://{addr}"


def make_jwt(cfg: dict) -> str:
    pem = cfg["api"]["private_key"].encode()
    private_key = load_pem_private_key(pem, password=None)
    now = int(time.time())
    payload = {
        "publicKey": cfg["api"]["public_key"],
        "service": True,
        "iat": now,
        "exp": now + 3600,
    }
    return jwt.encode(payload, private_key, algorithm="EdDSA")


# ── Param / Endpoint model ────────────────────────────────────────────────────


class Param:
    """A single prompt-able parameter."""

    def __init__(
        self,
        key: str,
        label: str,
        required: bool = True,
        default: str = "",
        help: str = "",
    ):
        self.key = key
        self.label = label
        self.required = required
        self.default = default
        self.help = help

    def prompt(self) -> str | None:
        parts = [f"  {col(self.label, CYAN)}"]
        if self.help:
            parts.append(col(f" ({self.help})", DIM))
        if self.default:
            parts.append(col(f" [{self.default}]", DIM))
        if not self.required:
            parts.append(col(" optional", DIM))
        parts.append(": ")

        val = input("".join(parts)).strip()
        if not val:
            if self.default:
                return self.default
            if self.required:
                print(col(f"  ✗ '{self.label}' is required", RED))
                sys.exit(1)
            return None
        return val


class Endpoint:
    def __init__(
        self,
        name: str,
        method: str,
        path: str,
        params: list[Param],
        description: str = "",
    ):
        self.name = name
        self.method = method
        self.path = path
        self.params = params
        self.description = description

    def collect(self) -> tuple[str, dict]:
        """Prompt for all params. Returns (resolved_path, body_dict)."""
        values: dict[str, str] = {}
        for p in self.params:
            val = p.prompt()
            if val is not None:
                values[p.key] = val

        # Substitute path params like {trackingId}
        resolved = self.path
        body: dict[str, str] = {}
        for key, val in values.items():
            placeholder = f"{{{key}}}"
            if placeholder in resolved:
                resolved = resolved.replace(placeholder, val)
            else:
                body[key] = val

        return resolved, body


# ── Endpoint registry ─────────────────────────────────────────────────────────
#
# To add a new endpoint, append an Endpoint(...) entry to this list.
# Path params use {braces} and are automatically spliced into the URL;
# everything else becomes the JSON body (or query string for GET).

ENDPOINTS: list[Endpoint] = [

    # ── Smart contract deployments ────────────────────────────────────────────

    Endpoint(
        name="Deploy ERC20 Token",
        method="POST",
        path="/api/v1/contracts/token",
        description="Queue a new ERC20 token deployment. Returns a trackingId.",
        params=[
            Param("name", "Token name"),
            Param("symbol", "Token symbol"),
            Param("decimals", "Decimals", default="18", help="0-255"),
            Param("owner", "Owner address", help="0x… checksummed"),
            Param(
                "expiryTimestamp",
                "Expiry timestamp",
                required=False,
                help="Unix epoch string",
            ),
        ],
    ),

    # ── Status / check endpoints ──────────────────────────────────────────────
    # Example — uncomment and adjust path/params when the endpoint exists:
    #
    # Endpoint(
    #     name="Get Transaction Status",
    #     method="GET",
    #     path="/api/v1/transactions/{trackingId}",
    #     description="Check the dispatch status of a queued transaction.",
    #     params=[
    #         Param("trackingId", "Tracking ID", help="UUID returned by deploy"),
    #     ],
    # ),
]

# ── Runner ────────────────────────────────────────────────────────────────────


def select_endpoint() -> Endpoint:
    print(col("\nEndpoints", BOLD))
    hr()
    pad = len(str(len(ENDPOINTS)))
    for i, ep in enumerate(ENDPOINTS, 1):
        num = col(str(i).rjust(pad), YELLOW)
        method = col(ep.method.ljust(6), MAGENTA)
        path = col(ep.path, CYAN)
        name = col(f"  {ep.name}", DIM)
        print(f"  {num}.  {method} {path}{name}")
    hr()

    raw = input(
        col(f"Select [1–{len(ENDPOINTS)}]: ", BOLD)
    ).strip()
    try:
        idx = int(raw) - 1
        if not 0 <= idx < len(ENDPOINTS):
            raise ValueError
    except ValueError:
        print(col("Invalid selection.", RED))
        sys.exit(1)

    return ENDPOINTS[idx]


def print_block(label: str, data: Any) -> None:
    print(col(f"\n{label}", BOLD))
    hr()
    print(json.dumps(data, indent=2))
    hr()


def send(
    method: str,
    url: str,
    headers: dict,
    body: dict,
) -> requests.Response:
    kwargs: dict = {"headers": headers, "timeout": 30}
    if method == "GET":
        kwargs["params"] = body
    else:
        kwargs["json"] = body

    return requests.request(method, url, **kwargs)


def run() -> None:
    cfg = load_config()
    url_base = base_url(cfg)
    token = make_jwt(cfg)
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
    }

    ep = select_endpoint()

    if ep.description:
        print(col(f"\n  {ep.description}", DIM))

    print(
        col(f"\n  {ep.method} ", MAGENTA)
        + col(url_base, DIM)
        + col(ep.path, CYAN)
    )
    print()

    resolved_path, body = ep.collect()
    full_url = url_base + resolved_path

    print_block("Payload", body)
    confirm = input(col("Send? [Y/n]: ", BOLD)).strip().lower()
    if confirm not in ("", "y", "yes"):
        print("Aborted.")
        return

    t0 = time.perf_counter()
    try:
        resp = send(ep.method, full_url, headers, body)
    except requests.RequestException as exc:
        print(col(f"\nRequest error: {exc}", RED))
        sys.exit(1)
    elapsed = (time.perf_counter() - t0) * 1000

    status_col = GREEN if resp.status_code < 400 else RED
    print(
        col("\nResponse  ", BOLD)
        + col(f"HTTP {resp.status_code}", status_col)
        + col(f"  {elapsed:.0f}ms", DIM)
    )
    hr()
    try:
        print(json.dumps(resp.json(), indent=2))
    except ValueError:
        print(resp.text)
    hr()
    print()


if __name__ == "__main__":
    run()
