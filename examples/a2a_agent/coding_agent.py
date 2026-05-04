# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "a2a-sdk>=1.0.0",
#   "google-adk>=1.31.0",
#   "google-genai>=1.0.0",
#   "fastapi>=0.115",
#   "uvicorn>=0.30",
#   "grpcio>=1.66",
# ]
# ///
"""Sample Coding A2A agent backed by a Google ADK LlmAgent.

Exposes an ADK Gemini agent over the A2A protocol on three transports
(gRPC, JSON-RPC, HTTP+JSON REST).

What it does:
  - Writes Python code on request.
  - Before saving anything, the agent proposes a full filesystem path
    (directory + filename) and waits for the user's explicit confirmation.
    Files are only written after the user replies "yes". The agent picks
    the path each turn; there is no fixed sandbox - the user's confirmation
    is the only gate.
  - Returns the saved Python file as a FilePart (``text/x-python``)
    alongside the agent's text reply, so clients can surface the actual
    generated source.
  - Optionally enforces auth (``--auth``): when enabled, the AgentCard
    advertises both Bearer and API-key schemes as alternatives, and the
    server accepts either credential on every request.

The example uses a small in-process ``AdkAgentExecutor`` adapter rather than
``google.adk.a2a.executor.A2aAgentExecutor`` because the latter (in
google-adk 1.31.x) is pinned to a2a-sdk 0.3.x while this example targets
a2a-sdk 1.0.x. The adapter implements the A2A long-running / INPUT_REQUIRED
flow on top of ADK's ``LongRunningFunctionTool`` semantics.

Auth (set one before launching):
  * Gemini API key:  export GOOGLE_API_KEY=...
  * Vertex AI:       export GOOGLE_GENAI_USE_VERTEXAI=TRUE
                     export GOOGLE_CLOUD_PROJECT=<project>
                     export GOOGLE_CLOUD_LOCATION=<region>

Run:
    uv run examples/a2a_agent/coding_agent.py
    # Demo polling fallback by disabling streaming in the AgentCard:
    uv run examples/a2a_agent/coding_agent.py --no-streaming
"""

import argparse
import asyncio
import contextlib
import logging
import os
import secrets

from pathlib import Path
from typing import Literal

import grpc
import uvicorn

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

from a2a.server.agent_execution.agent_executor import AgentExecutor
from a2a.server.agent_execution.context import RequestContext
from a2a.server.events.event_queue import EventQueue
from a2a.server.request_handlers import DefaultRequestHandler, GrpcHandler
from a2a.server.request_handlers.response_helpers import agent_card_to_dict
from a2a.server.routes import (
    create_jsonrpc_routes,
    create_rest_routes,
)
from a2a.server.tasks.inmemory_task_store import InMemoryTaskStore
from a2a.server.tasks.task_updater import TaskUpdater
from a2a.utils.constants import AGENT_CARD_WELL_KNOWN_PATH
from a2a.types import (
    AgentCapabilities,
    AgentCard,
    AgentInterface,
    AgentSkill,
    APIKeySecurityScheme,
    HTTPAuthSecurityScheme,
    Part,
    SecurityRequirement,
    SecurityScheme,
    StringList,
    Task,
    TaskState,
    TaskStatus,
    a2a_pb2_grpc,
)

from google.adk.agents import Agent
from google.adk.artifacts.in_memory_artifact_service import InMemoryArtifactService
from google.adk.events.event import Event as AdkEvent
from google.adk.memory.in_memory_memory_service import InMemoryMemoryService
from google.adk.runners import Runner
from google.adk.sessions.in_memory_session_service import InMemorySessionService
from google.adk.tools.long_running_tool import LongRunningFunctionTool
from google.genai import types as genai_types


logger = logging.getLogger(__name__)


# Synonyms used to interpret the user's reply to a save-confirmation prompt.
_AFFIRMATIVE = {"yes", "ok", "confirm", "go ahead", "approve", "approved"}
_NEGATIVE = {"no", "nope", "stop", "abort", "decline", "declined"}


def _check_http_credential(
    request: Request,
    expected_token: str,
    api_key_header: str,
) -> bool:
    """Validates a credential carried in HTTP headers (Bearer OR API key).

    Uses secrets.compare_digest for constant-time comparison so the auth
    decision does not leak the token via response-time differences.
    """
    v = request.headers.get("authorization", "")
    if v.startswith("Bearer ") and secrets.compare_digest(
        v[len("Bearer ") :], expected_token
    ):
        return True
    return secrets.compare_digest(
        request.headers.get(api_key_header, ""), expected_token
    )


class _GrpcAuthInterceptor(grpc.aio.ServerInterceptor):
    """Rejects gRPC calls without a valid Bearer or API-key credential in metadata."""

    def __init__(self, expected_token: str, api_key_header: str) -> None:
        self._expected_token = expected_token
        # gRPC normalizes metadata keys to lowercase.
        self._api_key_header_lower = api_key_header.lower()

    async def intercept_service(self, continuation, handler_call_details):
        meta = dict(handler_call_details.invocation_metadata or [])
        v = meta.get("authorization", "")
        bearer_ok = v.startswith("Bearer ") and secrets.compare_digest(
            v[len("Bearer ") :], self._expected_token
        )
        apikey_ok = secrets.compare_digest(
            meta.get(self._api_key_header_lower, ""), self._expected_token
        )
        if bearer_ok or apikey_ok:
            return await continuation(handler_call_details)

        async def _abort(unused_request, context):
            await context.abort(
                grpc.StatusCode.UNAUTHENTICATED, "missing or invalid credential"
            )

        return grpc.unary_unary_rpc_method_handler(_abort)


def propose_save_python_file(path: str, code: str) -> dict:
    """Stage a Python file for saving; pauses for user confirmation.

    Args:
      path: Filesystem path where the file should be written. May be relative
        (e.g. './scripts/hello.py'), absolute ('/tmp/hello.py'), or use '~' to
        refer to the user's home directory ('~/Desktop/hello.py'). Must end
        in '.py'. Parent directories are created if missing on confirmation.
      code: The Python source to write.

    Returns:
      A status dict. The actual write happens after the user confirms; the
      eventual response is {"status": "saved", "path": "..."},
      {"status": "declined"}, or {"status": "error", "message": "..."}.
    """
    return {"status": "pending_confirmation", "path": path}


class AdkAgentExecutor(AgentExecutor):
    """A2A AgentExecutor that bridges ADK's long-running tool semantics to
    A2A's TASK_STATE_INPUT_REQUIRED flow.

    Each A2A context is mapped 1:1 to an ADK session. When the agent calls
    ``propose_save_python_file`` the task is paused; on the user's next
    message the executor parses yes/no, performs (or skips) the file write,
    and resumes ADK by feeding back a matching ``function_response``.
    """

    USER_ID = "a2a_user"

    def __init__(self, runner: Runner) -> None:
        self._runner = runner
        self._running_tasks: set[str] = set()
        # task_id -> {function_call_id, name, path, code}
        self._pending_confirmations: dict[str, dict] = {}
        # task_id -> resolved Path of a file just saved this turn (for FilePart attachment).
        self._saved_files: dict[str, Path] = {}
        # task_id -> proposed code that the user declined to save (for text Part attachment).
        self._declined_codes: dict[str, str] = {}

    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        """Marks an in-flight task as cancelled."""
        task_id = context.task_id
        if task_id in self._running_tasks:
            self._running_tasks.remove(task_id)
        self._pending_confirmations.pop(task_id or "", None)
        self._saved_files.pop(task_id or "", None)
        self._declined_codes.pop(task_id or "", None)

        updater = TaskUpdater(
            event_queue=event_queue,
            task_id=task_id or "",
            context_id=context.context_id or "",
        )
        await updater.cancel()

    async def execute(self, context: RequestContext, event_queue: EventQueue) -> None:
        """Runs the ADK agent for the incoming A2A request."""
        user_message = context.message
        task_id = context.task_id
        context_id = context.context_id

        if not user_message or not task_id or not context_id:
            return

        self._running_tasks.add(task_id)

        # Branch A: follow-up reply for a paused (INPUT_REQUIRED) task.
        pending = self._pending_confirmations.get(task_id)
        is_resume = (
            pending is not None
            and context.current_task is not None
            and context.current_task.status.state == TaskState.TASK_STATE_INPUT_REQUIRED
        )

        updater = TaskUpdater(
            event_queue=event_queue, task_id=task_id, context_id=context_id
        )

        if is_resume:
            user_text = (context.get_user_input() or "").strip()
            decision = self._parse_decision(user_text)

            if decision is None:
                # Ambiguous reply - keep the task paused and re-prompt.
                await updater.update_status(
                    state=TaskState.TASK_STATE_INPUT_REQUIRED,
                    message=updater.new_agent_message(
                        parts=[
                            Part(
                                text=(
                                    f"I didn't catch that. Reply `yes` to save the file to "
                                    f"`{pending['path']}` or `no` to skip."
                                )
                            )
                        ]
                    ),
                )
                return

            if decision == "approved":
                save_result = await self._save_file(
                    task_id, pending["path"], pending["code"]
                )
            else:
                save_result = {"status": "declined"}
                self._declined_codes[task_id] = pending["code"]

            self._pending_confirmations.pop(task_id, None)

            new_message = genai_types.Content(
                role="user",
                parts=[
                    genai_types.Part(
                        function_response=genai_types.FunctionResponse(
                            id=pending["function_call_id"],
                            name=pending["name"],
                            response=save_result,
                        )
                    )
                ],
            )
        else:
            # Branch B: fresh turn. Emit submitted/working events.
            await event_queue.enqueue_event(
                Task(
                    id=task_id,
                    context_id=context_id,
                    status=TaskStatus(state=TaskState.TASK_STATE_SUBMITTED),
                    history=[user_message],
                )
            )
            await updater.start_work(
                message=updater.new_agent_message(
                    parts=[Part(text="Processing your question...")],
                )
            )

            user_text = context.get_user_input() or ""
            new_message = genai_types.Content(
                role="user",
                parts=[genai_types.Part(text=user_text)],
            )

        # Drive ADK and watch for long-running calls + text events.
        pending_call: dict | None = None
        final_chunks: list[str] = []
        try:
            async for adk_event in self._runner.run_async(
                user_id=self.USER_ID,
                session_id=context_id,
                new_message=new_message,
            ):
                if task_id not in self._running_tasks:
                    return

                # Capture the first long-running tool call we see, if any.
                if pending_call is None and adk_event.long_running_tool_ids:
                    pending_call = self._capture_long_running_call(adk_event)

                event_text = self._extract_text(adk_event)
                if not event_text:
                    continue

                if adk_event.is_final_response():
                    final_chunks.append(event_text)
                else:
                    await updater.update_status(
                        state=TaskState.TASK_STATE_WORKING,
                        message=updater.new_agent_message(
                            parts=[Part(text=event_text)]
                        ),
                    )
        except Exception as exc:  # noqa: BLE001
            logger.exception("[AdkAgentExecutor] ADK run failed for task %s", task_id)
            await updater.failed(
                message=updater.new_agent_message(parts=[Part(text=str(exc))])
            )
            self._running_tasks.discard(task_id)
            self._pending_confirmations.pop(task_id, None)
            self._saved_files.pop(task_id, None)
            self._declined_codes.pop(task_id, None)
            return

        if task_id not in self._running_tasks:
            return

        # If the agent proposed a save, pause the task in INPUT_REQUIRED.
        if pending_call is not None:
            proposed_path = pending_call["args"].get("path", "")
            proposed_code = pending_call["args"].get("code", "")
            resolved = self._resolve_path(proposed_path)

            self._pending_confirmations[task_id] = {
                "function_call_id": pending_call["id"],
                "name": pending_call["name"],
                "path": str(resolved),
                "code": proposed_code,
            }

            line_count = proposed_code.count("\n") + (
                1 if proposed_code and not proposed_code.endswith("\n") else 0
            )
            preview = (
                f"I'd like to save this Python code to `{resolved}` "
                f"({len(proposed_code)} chars, {line_count} lines).\n"
                "The full source will be returned as an attachment after you approve."
            )
            await updater.update_status(
                state=TaskState.TASK_STATE_INPUT_REQUIRED,
                message=updater.new_agent_message(parts=[Part(text=preview)]),
            )
            self._running_tasks.discard(task_id)
            logger.info(
                "[AdkAgentExecutor] Task %s paused for save confirmation: %s",
                task_id,
                resolved,
            )
            return

        # Completion path: text reply + saved-file FilePart, or proposed-code text Part on decline.
        final_text = "".join(final_chunks).strip() or "(no response)"
        parts: list[Part] = [Part(text=final_text)]

        saved_path = self._saved_files.pop(task_id, None)
        declined_code = self._declined_codes.pop(task_id, None)

        if saved_path is not None:
            try:
                file_bytes = saved_path.read_bytes()
                parts.append(
                    Part(
                        raw=file_bytes,
                        media_type="text/x-python",
                        filename=saved_path.name,
                    )
                )
            except OSError as exc:
                logger.warning(
                    "[AdkAgentExecutor] Could not attach saved file %s: %s",
                    saved_path,
                    exc,
                )
        elif declined_code is not None:
            parts.append(Part(text=f"```python\n{declined_code}\n```"))

        await updater.add_artifact(parts=parts, name="response", last_chunk=True)
        await updater.complete()
        self._running_tasks.discard(task_id)

    # Helpers

    @staticmethod
    def _extract_text(event: AdkEvent) -> str:
        """Concatenates text parts on an ADK event, ignoring tool calls etc."""
        content = getattr(event, "content", None)
        if not content or not getattr(content, "parts", None):
            return ""
        return "".join(
            part.text for part in content.parts if getattr(part, "text", None)
        )

    @staticmethod
    def _capture_long_running_call(event: AdkEvent) -> dict | None:
        """Returns ``{id, name, args}`` for the first long-running call, if any."""
        ids = event.long_running_tool_ids or set()
        content = getattr(event, "content", None)
        if not ids or not content or not getattr(content, "parts", None):
            return None
        for part in content.parts:
            fc = getattr(part, "function_call", None)
            if fc and fc.id in ids:
                return {"id": fc.id, "name": fc.name, "args": dict(fc.args or {})}
        return None

    @staticmethod
    def _parse_decision(text: str) -> Literal["approved", "declined"] | None:
        """Classifies a user reply as approved / declined / ambiguous."""
        normalized = text.strip().lower().rstrip(".!?")
        if not normalized:
            return None
        if normalized in _AFFIRMATIVE:
            return "approved"
        if normalized in _NEGATIVE:
            return "declined"
        # Allow simple prefix matches ("yes please save it", "no thanks").
        first_word = normalized.split(maxsplit=1)[0]
        if first_word in _AFFIRMATIVE:
            return "approved"
        if first_word in _NEGATIVE:
            return "declined"
        return None

    @staticmethod
    def _resolve_path(raw_path: str) -> Path:
        """Expands ~ and resolves the path without requiring it to exist."""
        return Path(raw_path).expanduser().resolve(strict=False)

    async def _save_file(self, task_id: str, raw_path: str, code: str) -> dict:
        """Writes ``code`` to ``raw_path`` and stashes the resolved Path so the
        caller can attach it as a FilePart."""
        if not (raw_path or "").strip():
            return {"status": "error", "message": "No path provided."}
        try:
            resolved = self._resolve_path(raw_path)
            if not resolved.is_relative_to(Path.home()):
                return {
                    "status": "error",
                    "message": f"Refusing to save to '{resolved}': path must be within the home directory.",
                }
        except (OSError, RuntimeError, ValueError) as exc:
            return {"status": "error", "message": f"Invalid path: {exc}"}
        if resolved.suffix != ".py":
            return {
                "status": "error",
                "message": f"Refusing to save '{resolved}': filename must end in .py.",
            }

        # Hardcoded 1-second delay simulates long-running work so clients can
        # exercise polling-fallback or long streaming-status code paths.
        await asyncio.sleep(1)

        try:
            resolved.parent.mkdir(parents=True, exist_ok=True)
            resolved.write_text(code, encoding="utf-8")
        except OSError as exc:
            return {"status": "error", "message": f"Failed to write file: {exc}"}

        self._saved_files[task_id] = resolved
        logger.info("[AdkAgentExecutor] Saved %d bytes to %s", len(code), resolved)
        return {"status": "saved", "path": str(resolved)}


async def serve(
    host: str = "127.0.0.1",
    port: int = 41241,
    grpc_port: int = 50051,
    streaming: bool = True,
    auth_enabled: bool = False,
    expected_token: str = "",
    api_key_header: str = "X-API-Key",
) -> None:
    """Run the Coding Agent server with mounted JSON-RPC, HTTP+JSON and gRPC transports."""
    jsonrpc_url = f"http://{host}:{port}/a2a/jsonrpc"
    rest_url = f"http://{host}:{port}/a2a/rest"
    supported_interfaces = [
        AgentInterface(
            protocol_binding="GRPC", protocol_version="1.0", url=f"{host}:{grpc_port}"
        ),
        AgentInterface(
            protocol_binding="JSONRPC", protocol_version="1.0", url=jsonrpc_url
        ),
        AgentInterface(
            protocol_binding="HTTP+JSON", protocol_version="1.0", url=rest_url
        ),
    ]

    # Optional auth: when enabled, advertise BOTH Bearer and API key on the
    # AgentCard as alternative requirements (OR semantics: client may use
    # either). Enforcement is symmetric (HTTP middleware + gRPC interceptor
    # accept either credential).
    security_schemes: dict = {}
    security_requirements: list = []
    if auth_enabled:
        security_schemes = {
            "bearerAuth": SecurityScheme(
                http_auth_security_scheme=HTTPAuthSecurityScheme(
                    scheme="bearer",
                    description="Bearer token in Authorization header.",
                )
            ),
            "apiKey": SecurityScheme(
                api_key_security_scheme=APIKeySecurityScheme(
                    location="header",
                    name=api_key_header,
                    description=f"API key sent in the {api_key_header} header.",
                )
            ),
        }
        # Two SecurityRequirement entries = OR (caller satisfies either alone),
        # not one entry with both keys (which would mean AND, requiring both).
        security_requirements = [
            SecurityRequirement(schemes={"bearerAuth": StringList(list=[])}),
            SecurityRequirement(schemes={"apiKey": StringList(list=[])}),
        ]

    agent_card = AgentCard(
        name="Coding Agent",
        description=(
            "A sample ADK agent that writes Python code and saves it to disk after user "
            "confirmation. Saved files are returned as a FilePart attachment alongside the "
            "agent's text reply."
        ),
        version="1.0.0",
        capabilities=AgentCapabilities(streaming=streaming, push_notifications=False),
        default_input_modes=["text"],
        default_output_modes=["text", "file", "task-status"],
        skills=[
            AgentSkill(
                id="coding_agent",
                name="Coding Agent",
                description=(
                    "Write Python scripts and save them to a path of your choice after you "
                    "confirm. The saved file is returned as a text/x-python attachment."
                ),
                tags=["coding", "python", "code-generation"],
                examples=[
                    "write me a hello world script",
                    "make a small fizzbuzz",
                    "yes save it",
                ],
                input_modes=["text"],
                output_modes=["text", "file", "task-status"],
            )
        ],
        supported_interfaces=supported_interfaces,
        security_schemes=security_schemes,
        security_requirements=security_requirements,
    )

    adk_agent = Agent(
        name="coding_agent",
        model="gemini-3-flash-preview",
        instruction="""You are a friendly Python coding assistant.

When the user asks you to write or save Python code:
  1. Reply with the full code in a fenced ```python block so the user can read it.
  2. To save the code, ALWAYS call the `propose_save_python_file` tool. You must pick the
     path yourself - include both the directory and the filename. Use the user's hint if
     they gave one (e.g. 'save it to my Desktop' -> '~/Desktop/<name>.py'). Otherwise
     default to './agent_output/<name>.py' relative to the server's working directory.
     Pick a short, descriptive filename ending in `.py`.
  3. The tool itself triggers a confirmation prompt for the user; do NOT ask about saving
     in plain text yourself.
  4. The tool's response says whether the user approved (with the resolved path) or
     declined. React briefly: a one-liner confirming the save, or a one-liner acknowledging
     the decline. On decline, do NOT repeat the proposed code in your reply - the user will
     see it in a separate response part.

For non-coding messages, just respond conversationally and do not call any tools.""",
        tools=[LongRunningFunctionTool(propose_save_python_file)],
    )
    runner = Runner(
        app_name="coding_agent",
        agent=adk_agent,
        session_service=InMemorySessionService(),
        artifact_service=InMemoryArtifactService(),
        memory_service=InMemoryMemoryService(),
        # The A2A context_id is used directly as the ADK session_id; let the
        # runner create the session on first use instead of pre-provisioning.
        auto_create_session=True,
    )

    task_store = InMemoryTaskStore()
    request_handler = DefaultRequestHandler(
        agent_executor=AdkAgentExecutor(runner=runner),
        task_store=task_store,
        agent_card=agent_card,
    )

    app = FastAPI()

    if auth_enabled:

        @app.middleware("http")
        async def _enforce_auth(request: Request, call_next):
            # Keep agent-card discovery unauthenticated so clients can read the
            # card to learn how to authenticate.
            if request.url.path.startswith("/.well-known/"):
                return await call_next(request)
            if not _check_http_credential(request, expected_token, api_key_header):
                # RFC 7235 allows multiple challenges in one WWW-Authenticate value.
                return JSONResponse(
                    {"error": "unauthorized"},
                    status_code=401,
                    headers={
                        "WWW-Authenticate": f'Bearer, ApiKey realm="{api_key_header}"'
                    },
                )
            return await call_next(request)

    @app.get(AGENT_CARD_WELL_KNOWN_PATH)
    async def _serve_agent_card():
        """Serve the AgentCard with a workaround for a Python <-> Go SDK shape
        mismatch: a2a-sdk Python emits SecurityRequirement.schemes[<name>] as
        the empty `{}` object (the StringList proto wrapper, with `list`
        elided), but a2a-go expects a bare JSON array `[]`. Walk the response
        and convert any dict value to its `list` contents (or [] when missing).
        Becomes a no-op once either SDK fixes the underlying serialization."""
        d = agent_card_to_dict(agent_card)
        for owner in (d, *d.get("skills", [])):
            for req in owner.get("securityRequirements", []) or []:
                for name, val in list(req.get("schemes", {}).items()):
                    if isinstance(val, dict):
                        req["schemes"][name] = val.get("list", [])
        return JSONResponse(d)

    for routes in (
        create_jsonrpc_routes(
            request_handler=request_handler,
            rpc_url="/a2a/jsonrpc",
        ),
        create_rest_routes(
            request_handler=request_handler,
            path_prefix="/a2a/rest",
        ),
    ):
        app.routes.extend(routes)

    grpc_interceptors: list = []
    if auth_enabled:
        grpc_interceptors.append(_GrpcAuthInterceptor(expected_token, api_key_header))
    grpc_server = grpc.aio.server(interceptors=grpc_interceptors)
    grpc_server.add_insecure_port(f"{host}:{grpc_port}")
    a2a_pb2_grpc.add_A2AServiceServicer_to_server(
        GrpcHandler(request_handler), grpc_server
    )

    uvicorn_server = uvicorn.Server(uvicorn.Config(app, host=host, port=port))

    logger.info(
        "Starting Coding Agent: HTTP http://%s:%s, gRPC %s:%s, "
        "AgentCard http://%s:%s/.well-known/agent-card.json",
        host,
        port,
        host,
        grpc_port,
        host,
        port,
    )

    await asyncio.gather(
        grpc_server.start(),
        uvicorn_server.serve(),
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Coding A2A agent server")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=41241)
    parser.add_argument("--grpc-port", type=int, default=50051)
    parser.add_argument(
        "--no-streaming",
        action="store_true",
        help="Disable streaming in the AgentCard (clients fall back to non-streaming polling).",
    )
    parser.add_argument(
        "--log-level",
        default="INFO",
        choices=["DEBUG", "INFO", "WARNING", "ERROR"],
        help="Logging verbosity (default: INFO).",
    )
    parser.add_argument(
        "--auth",
        action="store_true",
        help=(
            "Enable auth: advertises both Bearer and API key on the AgentCard "
            "and accepts either credential on incoming requests."
        ),
    )
    parser.add_argument(
        "--auth-token-env",
        default="CODING_AGENT_AUTH_TOKEN",
        help=(
            "Env var that holds the expected token / API key value (used when "
            "--auth is set)."
        ),
    )
    parser.add_argument(
        "--api-key-header",
        default="X-API-Key",
        help=(
            "Header name advertised on the AgentCard's APIKeySecurityScheme.name. "
            "Clients (including ax) read the name from the card and use it on "
            "outgoing requests."
        ),
    )
    args = parser.parse_args()
    logging.basicConfig(level=getattr(logging, args.log_level))

    expected_token = ""
    if args.auth:
        expected_token = os.environ.get(args.auth_token_env, "")
        if not expected_token:
            parser.error(
                f"--auth requires environment variable {args.auth_token_env} to be set."
            )

    with contextlib.suppress(KeyboardInterrupt):
        asyncio.run(
            serve(
                host=args.host,
                port=args.port,
                grpc_port=args.grpc_port,
                streaming=not args.no_streaming,
                auth_enabled=args.auth,
                expected_token=expected_token,
                api_key_header=args.api_key_header,
            )
        )
