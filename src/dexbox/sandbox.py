"""Sandbox orchestrator — manages Docker container lifecycle for workflow execution"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import docker.models.containers
from docker.errors import ContainerError, ImageNotFound

import docker
from dexbox.config import (
    ENV_PARENT_URL,
    ENV_SESSION_TOKEN,
    PARENT_URL,
    SANDBOX_CPU_QUOTA,
    SANDBOX_IMAGE,
    SANDBOX_MEMORY_LIMIT,
    SANDBOX_PULL_POLICY,
    SANDBOX_TMPFS_SIZE,
    STDOUT_MARKER,
)
from dexbox.logging import cleanup_sandbox_logging, setup_sandbox_logging
from dexbox.session import SessionManager

logger = logging.getLogger("dexbox.sandbox")


@dataclass(frozen=True)
class SandboxRuntime:
    """Configuration for a sandbox runtime environment."""

    image_env_var: str
    default_image: str
    command_prefix: tuple[str, ...]

    @property
    def image(self) -> str:
        return os.environ.get(self.image_env_var, self.default_image)


SANDBOX_RUNTIMES: dict[str, SandboxRuntime] = {
    ".py": SandboxRuntime(
        image_env_var="DEXBOX_SANDBOX_IMAGE",
        default_image=SANDBOX_IMAGE,
        command_prefix=("-c",),
    ),
}


def get_runtime_for_extension(ext: str) -> SandboxRuntime | None:
    return SANDBOX_RUNTIMES.get(ext.lower())


@dataclass
class WorkflowResult:
    """Result of a workflow execution."""

    success: bool
    exit_code: int
    logs: str
    error: str | None = None
    result: dict | None = None
    duration_ms: int = 0


def _get_harness() -> str:
    """Load the sandbox harness script."""
    harness_path = Path(__file__).parent / "harness" / "python.py"
    return harness_path.read_text()


class SandboxOrchestrator:
    """Orchestrates sandbox container execution."""

    def __init__(self, session_manager: SessionManager) -> None:
        self.session_manager = session_manager
        self._docker_client: docker.DockerClient | None = None
        self._active_container: docker.models.containers.Container | None = None
        self._active_session_token: str | None = None
        self._cancel_requested: bool = False

    @property
    def docker_client(self) -> docker.DockerClient:
        if self._docker_client is None:
            self._docker_client = docker.from_env()
        return self._docker_client

    def terminate_active_container(self) -> bool:
        """Kill the active sandbox container. Returns True if one was terminated."""
        if self._active_container is None:
            logger.info("No active sandbox container to terminate")
            return False
        container = self._active_container
        container_id = container.short_id
        try:
            if self._active_session_token:
                self.session_manager.terminate_session(self._active_session_token)
            try:
                container.kill()
                kill_succeeded = True
            except Exception as kill_err:
                logger.error("Error killing container %s: %s", container_id, kill_err)
                self._active_container = None
                self._active_session_token = None
                return False
            try:
                container.remove()
            except Exception as remove_err:
                logger.warning("Error removing container %s: %s", container_id, remove_err)
            self._active_container = None
            self._active_session_token = None
            return kill_succeeded
        except Exception as e:
            logger.error("Error terminating container %s: %s", container_id, e)
            self._active_container = None
            self._active_session_token = None
            return False

    def cancel_sandbox(self) -> bool:
        self._cancel_requested = True
        return self.terminate_active_container()

    async def execute_workflow(
        self,
        script: str,
        api_key: str,
        model: str = "claude-sonnet-4-5-20250929",
        provider: str = "anthropic",
        workflow_id: str = "",
        variables: dict[str, Any] | None = None,
        secure_params: dict[str, str] | None = None,
        runtime: SandboxRuntime | None = None,
        artifacts_dir: Path | None = None,
        session_token: str | None = None,
    ) -> WorkflowResult:
        """Execute a workflow script in a sandboxed container."""
        if runtime is None:
            runtime = SANDBOX_RUNTIMES[".py"]
        handlers: tuple | None = None
        self._cancel_requested = False
        try:
            if artifacts_dir:
                handlers = setup_sandbox_logging(artifacts_dir)

            if session_token is None:
                session_token = self.session_manager.create_session(
                    api_key=api_key,
                    model=model,
                    provider=provider,
                    workflow_id=workflow_id,
                    variables=variables,
                    secure_params=secure_params,
                    artifacts_dir=str(artifacts_dir) if artifacts_dir else None,
                )
            return await self._run_sandbox(
                script=script,
                session_token=session_token,
                workflow_id=workflow_id,
                runtime=runtime,
                variables=variables,
                artifacts_dir=artifacts_dir,
            )
        except ImageNotFound:
            return WorkflowResult(
                success=False,
                exit_code=-1,
                logs="",
                error=f"Sandbox image not found: {runtime.image}. Run 'make build-sandbox' first.",
            )
        except ContainerError as exc:
            return WorkflowResult(
                success=False, exit_code=exc.exit_status, logs=exc.stderr.decode() if exc.stderr else "", error=str(exc)
            )
        except Exception as exc:
            logger.exception("Workflow execution failed: %s", exc)
            return WorkflowResult(success=False, exit_code=-1, logs="", error=str(exc))
        finally:
            if session_token:
                self.session_manager.delete_session(session_token)
            if handlers:
                cleanup_sandbox_logging(*handlers)
            self._active_container = None
            self._active_session_token = None

    async def _run_sandbox(
        self,
        script: str,
        session_token: str,
        workflow_id: str,
        runtime: SandboxRuntime,
        variables: dict[str, Any] | None = None,
        artifacts_dir: Path | None = None,
    ) -> WorkflowResult:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(
            None, self._run_sandbox_sync, loop, script, session_token, workflow_id, runtime, variables, artifacts_dir
        )

    def _run_sandbox_sync(
        self,
        loop: asyncio.AbstractEventLoop,
        script: str,
        session_token: str,
        workflow_id: str,
        runtime: SandboxRuntime,
        variables: dict[str, Any] | None = None,
        artifacts_dir: Path | None = None,
    ) -> WorkflowResult:
        """Run sandbox container synchronously (runs in thread pool)."""
        logger.info("Starting sandbox container for workflow %s", workflow_id)
        start_time = time.time()
        harness_script = _get_harness()

        session = self.session_manager.get_session(session_token)
        if session:
            session.input_data = variables or {}
            session.code = script

        try:
            if SANDBOX_PULL_POLICY == "always":
                logger.info("Pulling image %s", runtime.image)
                self.docker_client.images.pull(runtime.image)

            container = self.docker_client.containers.run(
                image=runtime.image,
                command=[*runtime.command_prefix, harness_script],
                environment={
                    ENV_SESSION_TOKEN: session_token,
                    ENV_PARENT_URL: PARENT_URL,
                },
                network_mode="bridge",
                mem_limit=SANDBOX_MEMORY_LIMIT,
                cpu_quota=SANDBOX_CPU_QUOTA,
                detach=True,
                remove=False,
                cap_drop=["ALL"],
                security_opt=["no-new-privileges"],
                tmpfs={"/assets": f"size={SANDBOX_TMPFS_SIZE},mode=1777"},
            )

            self._active_container = container
            self._active_session_token = session_token
            logger.info("Container %s started", container.short_id)

            log_lines: list[str] = []
            workflow_log = artifacts_dir / "workflow.log" if artifacts_dir else None
            stdout_log = artifacts_dir / "stdout.log" if artifacts_dir else None
            if artifacts_dir:
                artifacts_dir.mkdir(parents=True, exist_ok=True)
                if session and session.input_data:
                    (artifacts_dir / "input.json").write_text(json.dumps(session.input_data, indent=2))

            try:
                for line in container.logs(stream=True, follow=True, stdout=True, stderr=True):
                    if self._cancel_requested:
                        break
                    line_str = line.decode("utf-8").rstrip()
                    if not line_str:
                        continue
                    log_lines.append(line_str)

                    # Stream log line to progress queue for real-time visibility
                    if session:
                        loop.call_soon_threadsafe(session.event_queue.put_nowait, {"type": "log", "data": line_str})

                    if artifacts_dir:
                        if line_str.startswith(STDOUT_MARKER):
                            with open(stdout_log, "a") as f:
                                f.write(line_str[len(STDOUT_MARKER) :] + "\n")
                        else:
                            with open(workflow_log, "a") as f:
                                f.write(line_str + "\n")
            except Exception as exc:
                logger.info("Container log stream ended: %s", exc)

            try:
                result = container.wait(timeout=2)
                exit_code = result.get("StatusCode", -1)
            except Exception:
                exit_code = -1

            # Write assets archive if sandbox uploaded one
            if artifacts_dir and session and session.assets_archive:
                try:
                    import io
                    import tarfile

                    tar_bytes = session.assets_archive
                    with tarfile.open(fileobj=io.BytesIO(tar_bytes)) as tf:
                        tf.extractall(artifacts_dir)
                except Exception as e:
                    logger.warning("Failed to extract assets archive: %s", e)

            output_data = None
            error_message = None
            if session and session.output_data:
                output_data = session.output_data
            else:
                logger.error("No output data from sandbox")
                if exit_code == 0:
                    exit_code = 1
                error_message = "Sandbox failed to return output data"

            if output_data and artifacts_dir:
                (artifacts_dir / "output.json").write_text(json.dumps(output_data, indent=2))

            handler_result = None
            if output_data:
                handler_result = output_data.get("result")
                if not output_data.get("success"):
                    error_message = output_data.get("error", "Unknown error")

            if self._active_container is not None:
                try:
                    container.remove()
                except Exception:
                    pass

            self._active_container = None
            self._active_session_token = None

            success = exit_code == 0
            duration_ms = int((time.time() - start_time) * 1_000)

            # System logging
            if success:
                logger.info(
                    "Container %s finished: exit_code=%s duration=%dms",
                    container.short_id,
                    exit_code,
                    duration_ms,
                )
            else:
                logger.warning(
                    "Container %s finished: exit_code=%s duration=%dms error=%s",
                    container.short_id,
                    exit_code,
                    duration_ms,
                    error_message,
                )

            # Workflow metadata logging (WARNING so it survives WorkflowViewFilter)
            logger.warning(
                "Workflow execution complete",
                extra={
                    "run ID": container.short_id,
                    "workflow ID": workflow_id,
                    "run_duration_ms": duration_ms,
                    "execution status": "success" if success else "failed",
                },
            )

            return WorkflowResult(
                success=success,
                exit_code=exit_code,
                logs="\n".join(log_lines),
                error=error_message or (None if success else f"Exit code: {exit_code}"),
                result=handler_result,
                duration_ms=duration_ms,
            )
        except Exception as exc:
            logger.exception("Sandbox execution failed: %s", exc)
            raise
