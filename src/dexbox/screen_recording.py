"""Screen recording server — FastAPI service that controls ffmpeg via supervisord."""

from __future__ import annotations

import http.client
import json
import logging
import shutil
import socket
import time
import xmlrpc.client
from pathlib import Path
from typing import Optional

import uvicorn
from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel

logger = logging.getLogger("dexbox.recorder")

SUPERVISOR_SOCKET = "/var/run/supervisor/supervisor.sock"
FFMPEG_PROCESS_NAME = "ffmpeg-recorder"
RECORDING_TMP_DIR = "/tmp/recordings"
METADATA_FILE = "/tmp/recording_metadata.json"


class UnixStreamHTTPConnection(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.connect(self.host)


class UnixStreamTransport(xmlrpc.client.Transport):
    def __init__(self, socket_path: str):
        self.socket_path = socket_path
        super().__init__()

    def make_connection(self, host):
        return UnixStreamHTTPConnection(self.socket_path)


class StartRecordingResponse(BaseModel):
    status: str
    recording_dir: Optional[str] = None


class StopRecordingResponse(BaseModel):
    status: str
    copied_file: Optional[str] = None
    recording_duration_ms: int = 0
    error: Optional[str] = None


class RecordingStatusResponse(BaseModel):
    name: str
    state: str
    description: str


app = FastAPI(title="screen-recording")
app.add_middleware(CORSMiddleware, allow_origins=["*"], allow_methods=["GET", "POST"], allow_headers=["*"])


def get_supervisor():
    transport = UnixStreamTransport(SUPERVISOR_SOCKET)
    return xmlrpc.client.ServerProxy("http://localhost", transport=transport)


def _save_metadata(recording_dir: str | None = None) -> None:
    data = {"recording_dir": recording_dir, "start_time": time.time() * 1000}
    Path(METADATA_FILE).write_text(json.dumps(data))


def _load_metadata() -> dict:
    try:
        return json.loads(Path(METADATA_FILE).read_text())
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def _clear_metadata() -> None:
    try:
        Path(METADATA_FILE).unlink()
    except FileNotFoundError:
        pass


def _copy_latest_recording(destination_dir: str) -> str | None:
    tmp = Path(RECORDING_TMP_DIR)
    if not tmp.exists():
        return None
    files = sorted(tmp.glob("*.mp4"), key=lambda f: f.stat().st_mtime)
    if not files:
        return None
    dest = Path(destination_dir)
    dest.mkdir(parents=True, exist_ok=True)
    target = dest / "recording.mp4"
    shutil.copy2(files[-1], target)
    return str(target)


@app.post("/record/start", response_model=StartRecordingResponse)
async def start_recording(recording_dir: str = None):
    try:
        sv = get_supervisor()
        sv.supervisor.startProcess(FFMPEG_PROCESS_NAME)
        _save_metadata(recording_dir)
        return StartRecordingResponse(status="started", recording_dir=recording_dir)
    except Exception as exc:
        logger.error("Failed to start recording: %s", exc)
        return StartRecordingResponse(status="error")


@app.post("/record/stop", response_model=StopRecordingResponse)
async def stop_recording():
    metadata = _load_metadata()
    start_ms = metadata.get("start_time", time.time() * 1000)
    recording_dir = metadata.get("recording_dir")
    duration_ms = int(time.time() * 1000 - start_ms)

    status = "stopped"
    error_detail: str | None = None
    try:
        sv = get_supervisor()
        sv.supervisor.stopProcess(FFMPEG_PROCESS_NAME)
    except Exception as exc:
        status = "error"
        error_detail = str(exc)
        logger.warning("Failed to stop ffmpeg via supervisord: %s", exc)

    copied = None
    if recording_dir:
        try:
            copied = _copy_latest_recording(recording_dir)
        except Exception as exc:
            logger.error("Failed to copy recording: %s", exc)

    _clear_metadata()
    return StopRecordingResponse(
        status=status,
        copied_file=copied,
        recording_duration_ms=duration_ms,
        error=error_detail,
    )


@app.get("/record/status", response_model=RecordingStatusResponse)
async def get_status():
    try:
        sv = get_supervisor()
        info = sv.supervisor.getProcessInfo(FFMPEG_PROCESS_NAME)
        return RecordingStatusResponse(
            name=FFMPEG_PROCESS_NAME, state=info.get("statename", "UNKNOWN"), description=info.get("description", "")
        )
    except Exception as exc:
        return RecordingStatusResponse(name=FFMPEG_PROCESS_NAME, state="ERROR", description=str(exc))


if __name__ == "__main__":
    import os

    log_level_str = os.environ.get("DEXBOX_LOG_LEVEL", "INFO").upper()
    log_level = getattr(logging, log_level_str, logging.INFO)

    handler = logging.StreamHandler()
    handler.setLevel(log_level)
    logging.basicConfig(level=logging.NOTSET, handlers=[handler])

    uvicorn.run(app, host="0.0.0.0", port=5001, log_level=log_level_str.lower())
