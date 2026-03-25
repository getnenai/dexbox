"""Thin wrapper around the shared Extend parse module."""

from __future__ import annotations

import sys
from pathlib import Path

from langchain_core.tools import StructuredTool
from pydantic import BaseModel, Field

_SHARED = Path(__file__).resolve().parent.parent.parent / "shared"
if str(_SHARED) not in sys.path:
    sys.path.insert(0, str(_SHARED))

from extend_parse import ExtendParseResult  # noqa: E402
from extend_parse import parse_document as _parse  # noqa: E402


def parse_document(file_path: str | Path) -> str:
    """Parse a document and return the markdown content."""
    return _parse(file_path).markdown


def parse_document_full(file_path: str | Path) -> ExtendParseResult:
    """Parse a document and return the full result with raw JSON."""
    return _parse(file_path)


class ParseDocumentInput(BaseModel):
    file_path: str = Field(
        description=(
            "Filename of the document to parse (e.g. 'invoice.pdf'). "
            "Files in the VM's \\\\vboxsvr\\shared\\ folder can be referenced "
            "by filename alone."
        ),
    )


def build_parse_tool() -> StructuredTool:
    """Build a LangChain StructuredTool for document parsing via Extend AI."""
    return StructuredTool.from_function(
        func=parse_document,
        name="parse_document",
        description=(
            "Parse a document (PDF, image, etc.) using Extend AI and return its "
            "content as structured markdown text. Use this to read and understand "
            "the contents of document files. "
            "This tool runs on the host machine, NOT inside the Windows VM. "
            "The shared folder between host and guest is the default lookup "
            "directory: files at \\\\vboxsvr\\shared\\ on the Windows VM are in "
            "this directory. Pass just the filename (e.g. 'invoice.pdf') for "
            "files in the shared folder, or an absolute host path for files "
            "elsewhere."
        ),
        args_schema=ParseDocumentInput,
    )
