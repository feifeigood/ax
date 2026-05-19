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
  - Saves the code to a file under examples/a2a_agent/output/.
  - Returns the saved file as a FilePart (``text/x-python``) alongside the
    agent's text reply, so clients can surface the actual generated source.
  - Optionally enforces auth (``--auth``): when enabled, the AgentCard
    advertises both Bearer and API-key schemes as alternatives, and the
    server accepts either credential on every request.

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
from google.genai import types as genai_types


logger = logging.getLogger(__name__)


# Generated files land here. Bare filenames only (no path components, no '..').
_OUTPUT_DIR = Path(__file__).resolve().parent / "output"


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


def save_python_file(filename: str, code: str) -> dict:
    """Save Python code to examples/a2a_agent/output/<filename>.

    Args:
      filename: Bare filename (no path components, no '..', must end in '.py').
      code: The Python source to write. Overwrites any existing file in the
        output directory.

    Returns:
      {"status": "saved", "path": "/abs/.../examples/a2a_agent/output/<filename>"}
      on success, or {"status": "error", "message": "..."} on validation failure.
    """
    name = (filename or "").strip()
    if not name or "/" in name or "\\" in name or ".." in name:
        return {"status": "error", "message": f"Invalid filename: {filename!r}"}
    if not name.endswith(".py"):
        return {"status": "error", "message": "Filename must end in .py"}
    target = (_OUTPUT_DIR / name).resolve()
    if target.parent != _OUTPUT_DIR:
        return {
            "status": "error",
            "message": "Filename must not contain path components",
        }
    try:
        _OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
        target.write_text(code, encoding="utf-8")
    except OSError as exc:
        return {"status": "error", "message": f"Failed to write file: {exc}"}
    logger.info("[save_python_file] Saved %d bytes to %s", len(code), target)
    return {"status": "saved", "path": str(target)}


class AdkAgentExecutor(AgentExecutor):
    """A2A AgentExecutor that runs a Google ADK LlmAgent.

    Each A2A context is mapped 1:1 to an ADK session. Each user turn is a
    single straight-through invocation of ``Runner.run_async``: the agent
    streams text (forwarded as TASK_STATE_WORKING status updates) and may
    call ``save_python_file`` inline; any successfully saved files are
    attached to the final artifact as text/x-python FileParts.
    """

    USER_ID = "a2a_user"

    def __init__(self, runner: Runner) -> None:
        self._runner = runner
        self._running_tasks: set[str] = set()

    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        """Marks an in-flight task as cancelled."""
        task_id = context.task_id
        if task_id in self._running_tasks:
            self._running_tasks.remove(task_id)

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

        updater = TaskUpdater(
            event_queue=event_queue, task_id=task_id, context_id=context_id
        )

        # Fresh turn: emit submitted/working events.
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

        new_message = genai_types.Content(
            role="user",
            parts=[genai_types.Part(text=context.get_user_input() or "")],
        )

        # Stream ADK events: forward text as WORKING updates and harvest the
        # paths of any files the save_python_file tool wrote.
        final_chunks: list[str] = []
        saved_paths: list[str] = []
        try:
            async for adk_event in self._runner.run_async(
                user_id=self.USER_ID,
                session_id=context_id,
                new_message=new_message,
            ):
                if task_id not in self._running_tasks:
                    return

                saved_paths.extend(self._extract_saved_paths(adk_event))

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
            return

        if task_id not in self._running_tasks:
            return

        # Build artifact: text reply + one FilePart per saved file (in call order).
        final_text = "".join(final_chunks).strip() or "(no response)"
        parts: list[Part] = [Part(text=final_text)]
        for raw_path in saved_paths:
            path = Path(raw_path)
            try:
                file_bytes = path.read_bytes()
                parts.append(
                    Part(
                        raw=file_bytes,
                        media_type="text/x-python",
                        filename=path.name,
                    )
                )
            except OSError as exc:
                logger.warning(
                    "[AdkAgentExecutor] Could not attach saved file %s: %s",
                    path,
                    exc,
                )

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
    def _extract_saved_paths(event: AdkEvent) -> list[str]:
        """Returns absolute paths from successful save_python_file responses on this event."""
        out: list[str] = []
        content = getattr(event, "content", None)
        if not content or not getattr(content, "parts", None):
            return out
        for part in content.parts:
            fr = getattr(part, "function_response", None)
            if not fr or fr.name != "save_python_file":
                continue
            resp = getattr(fr, "response", None) or {}
            if (
                isinstance(resp, dict)
                and resp.get("status") == "saved"
                and resp.get("path")
            ):
                out.append(resp["path"])
        return out


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
            "A sample ADK agent that writes Python code and saves it under "
            "examples/a2a_agent/output/. Saved files are returned as FilePart "
            "attachments alongside the agent's text reply."
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
                    "Write Python scripts. Each is saved to "
                    "examples/a2a_agent/output/<filename>.py and returned as a "
                    "text/x-python attachment."
                ),
                tags=["coding", "python", "code-generation"],
                examples=[
                    "write me a hello world script",
                    "make a small fizzbuzz",
                    "write a function that reverses a string",
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
        model="gemini-3.5-flash",
        instruction="""You are a Python coding assistant.

When the user asks you to write Python code:
  1. Reply with the full code in a fenced ```python block.
  2. Call `save_python_file(filename, code)` to persist the code. Pick a short,
     descriptive filename ending in `.py` (e.g. 'fizzbuzz.py'). Filenames must be
     bare names with no path components - the server saves them under
     examples/a2a_agent/output/.
  3. Briefly confirm the save in your reply, mentioning the filename.

For non-coding messages, respond conversationally without calling any tools.""",
        tools=[save_python_file],
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
