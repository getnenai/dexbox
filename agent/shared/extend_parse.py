"""Extend /parse integration — standalone CLI + importable module."""

from __future__ import annotations

import json
import os
import sys
from dataclasses import dataclass
from pathlib import Path

import httpx

API_BASE = "https://api.extend.ai"
API_VERSION = "2026-02-09"

TIMEOUT = httpx.Timeout(connect=10.0, read=300.0, write=30.0, pool=10.0)


@dataclass
class ExtendParseResult:
    raw: dict
    markdown: str
    output_path: Path


def parse_document(
    file_path: str | Path,
    *,
    api_key: str | None = None,
    shared_dir: str | Path | None = None,
) -> ExtendParseResult:
    api_key = api_key or os.environ.get("EXTEND_API_KEY")
    if not api_key:
        raise EnvironmentError(
            "EXTEND_API_KEY is not set. Provide it via env or api_key param."
        )

    shared_dir = Path(
        shared_dir or os.environ.get("DEXBOX_SHARED_DIR", Path.home() / ".dexbox" / "shared")
    ).expanduser()

    file_path = Path(file_path)
    abs_path = (file_path if file_path.is_absolute() else shared_dir / file_path).resolve()
    shared_root = shared_dir.resolve()
    try:
        abs_path.relative_to(shared_root)
    except ValueError:
        raise ValueError(f"Invalid file path (outside shared dir): {file_path}") from None

    if not abs_path.is_file():
        raise FileNotFoundError(f"File not found: {abs_path}")

    headers = {
        "Authorization": f"Bearer {api_key}",
        "x-extend-api-version": API_VERSION,
    }

    # Step 1: Upload
    import mimetypes

    mime_type = mimetypes.guess_type(str(abs_path))[0] or "application/octet-stream"
    with open(abs_path, "rb") as f:
        upload_res = httpx.post(
            f"{API_BASE}/files/upload",
            headers=headers,
            files={"file": (abs_path.name, f, mime_type)},
            timeout=TIMEOUT,
        )

    if upload_res.status_code >= 400:
        raise RuntimeError(
            f"Upload failed ({upload_res.status_code}): {upload_res.text}"
        )

    file_id = upload_res.json()["id"]

    # Step 2: Parse
    parse_res = httpx.post(
        f"{API_BASE}/parse",
        headers={**headers, "Content-Type": "application/json"},
        json={"file": {"id": file_id}},
        timeout=TIMEOUT,
    )

    if parse_res.status_code >= 400:
        raise RuntimeError(
            f"Parse failed ({parse_res.status_code}): {parse_res.text}"
        )

    raw = parse_res.json()

    output = raw.get("output") or {}
    chunks = output.get("chunks", []) if isinstance(output, dict) else []
    markdown = "\n\n".join(
        c.get("content", "") for c in chunks if isinstance(c, dict) and c.get("content")
    )

    # Write output
    parsed_dir = shared_dir / "parsed"
    parsed_dir.mkdir(parents=True, exist_ok=True)
    output_path = parsed_dir / f"{abs_path.name}.json"
    output_path.write_text(json.dumps(raw, indent=2))

    return ExtendParseResult(raw=raw, markdown=markdown, output_path=output_path)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: python extend_parse.py <filename>", file=sys.stderr)
        sys.exit(1)

    # Load dotenv from agent/.env (one dir up from shared/)
    try:
        from dotenv import load_dotenv

        load_dotenv(Path(__file__).resolve().parent.parent / ".env")
    except ImportError:
        pass

    try:
        result = parse_document(sys.argv[1])
        print(result.markdown)
    except Exception as e:
        print(str(e), file=sys.stderr)
        sys.exit(1)
