"""Local MCP proxy authenticated with the current Kerberos ticket."""

from __future__ import annotations

from collections.abc import Iterable
import os
from urllib.parse import urlsplit

import requests
import truststore
from flask import Flask, Response, abort, request, stream_with_context
from requests_gssapi import HTTPSPNEGOAuth


truststore.inject_into_ssl()


LISTEN_HOST = "127.0.0.1"
LISTEN_PORT = 8765
HOP_BY_HOP_HEADERS = frozenset(
    {
        "connection",
        "host",
        "keep-alive",
        "proxy-authenticate",
        "proxy-authorization",
        "te",
        "trailer",
        "trailers",
        "transfer-encoding",
        "upgrade",
    }
)

app = Flask(__name__)
session = requests.Session()
session.auth = HTTPSPNEGOAuth()


def configured_upstream_url() -> str:
    upstream_url = os.environ.get("MCP_UPSTREAM_URL", "").strip()
    url = urlsplit(upstream_url)
    if url.scheme not in {"http", "https"} or not url.netloc:
        raise RuntimeError(
            "MCP_UPSTREAM_URL must be an absolute HTTP(S) URL."
        )
    return upstream_url


UPSTREAM_URL = configured_upstream_url()


def forwarded_request_headers() -> dict[str, str]:
    return {
        name: value
        for name, value in request.headers.items()
        if name.lower() not in HOP_BY_HOP_HEADERS
    }


def forwarded_response_headers(response: requests.Response) -> dict[str, str]:
    return {
        name: value
        for name, value in response.headers.items()
        if name.lower() not in HOP_BY_HOP_HEADERS | {"content-length"}
    }


def response_body(response: requests.Response) -> Iterable[bytes]:
    return response.raw.stream(8192, decode_content=False)


@app.route("/mcp", methods=["GET", "POST", "DELETE"])
def proxy_mcp() -> Response:
    try:
        upstream_response = session.request(
            method=request.method,
            url=UPSTREAM_URL,
            headers=forwarded_request_headers(),
            data=request.get_data(),
            stream=True,
            timeout=(10, 600),
        )
    except requests.RequestException as error:
        app.logger.error("Upstream MCP request failed: %s", error)
        abort(502, "Upstream MCP endpoint is unavailable.")

    return Response(
        stream_with_context(response_body(upstream_response)),
        status=upstream_response.status_code,
        headers=forwarded_response_headers(upstream_response),
    )


if __name__ == "__main__":
    app.run(host=LISTEN_HOST, port=LISTEN_PORT, threaded=True)