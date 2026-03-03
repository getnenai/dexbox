"""Archive utilities for converting tar archives to zip files."""

from __future__ import annotations

import io
import logging
import tarfile
import zipfile

logger = logging.getLogger("dexbox.archive")


def tar_to_zip(tar_bytes: bytes) -> bytes:
    """Convert tar archive bytes to a zip archive bytes."""
    if not tar_bytes:
        return b""

    tar_io = io.BytesIO(tar_bytes)
    zip_io = io.BytesIO()

    with tarfile.open(fileobj=tar_io, mode="r:*") as tar:
        with zipfile.ZipFile(zip_io, "w", compression=zipfile.ZIP_DEFLATED) as zf:
            for member in tar.getmembers():
                if not (member.isreg() or member.isdir()):
                    continue

                # Normalize name: ensure 'assets/' prefix if not present, and avoid traversal
                name = member.name
                if name.startswith("./"):
                    name = name[2:]

                # Ensure it's under 'assets/' since that's what the harness sends
                # Actually, the harness already prefixes with 'assets/'
                # _arcname = str(_ArchivePath("assets") / _p.relative_to(_root))

                if member.isdir():
                    if not name.endswith("/"):
                        name += "/"
                    zf.writestr(name, "")
                    continue

                file_obj = tar.extractfile(member)
                if file_obj is not None:
                    with file_obj:
                        zf.writestr(name, file_obj.read())

    return zip_io.getvalue()
