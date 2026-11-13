#!/usr/bin/env python3
"""ADLC hook plugin: denies tool calls whose name matches a pattern."""
import json, os, re, signal, sys
from concurrent import futures

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "gen"))

import grpc  # noqa: E402
from adlc.v1 import events_pb2, plugin_pb2, plugin_pb2_grpc  # noqa: E402


class Hook(plugin_pb2_grpc.HookServiceServicer):
    def __init__(self, pattern, pattern_text, log_path):
        self.pattern = pattern
        self.pattern_text = pattern_text
        self.log_path = log_path

    def PreTool(self, request, context):
        name = request.info.call.name
        if self.pattern is not None and self.pattern.fullmatch(name):
            return plugin_pb2.PreToolResponse(
                decision=events_pb2.DECISION_DENY,
                reason=f"tool {name} matches deny pattern {self.pattern_text}",
            )
        return plugin_pb2.PreToolResponse(decision=events_pb2.DECISION_ALLOW)

    def PostTool(self, request, context):
        if self.log_path:
            line = json.dumps(
                {
                    "tool": request.info.call.name,
                    "is_error": request.is_error,
                    "exit_code": request.exit_code,
                }
            )
            with open(self.log_path, "a", encoding="utf-8") as f:
                f.write(line + "\n")
        return plugin_pb2.PostToolResponse()


def main() -> int:
    socket_path = os.environ.get("ADLC_PLUGIN_SOCKET")
    if not socket_path:
        print("ADLC_PLUGIN_SOCKET is not set", file=sys.stderr)
        return 2
    pattern_text = os.environ.get("ADLC_HOOK_DENY_PATTERN", "")
    pattern = None
    if pattern_text:
        try:
            pattern = re.compile(pattern_text)
        except re.error as e:
            print(f"invalid ADLC_HOOK_DENY_PATTERN: {e}", file=sys.stderr)
            return 2
    hook = Hook(pattern, pattern_text, os.environ.get("ADLC_HOOK_LOG", ""))

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    plugin_pb2_grpc.add_HookServiceServicer_to_server(hook, server)
    try:
        # Older grpcio returns 0 on failure; newer versions raise RuntimeError.
        bound = server.add_insecure_port("unix:" + socket_path)
    except RuntimeError:
        bound = 0
    if bound == 0:
        print(f"cannot listen on {socket_path}", file=sys.stderr)
        return 1
    server.start()

    def stop(signum, frame):
        server.stop(grace=1)
        sys.exit(0)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    server.wait_for_termination()
    return 0


if __name__ == "__main__":
    sys.exit(main())
