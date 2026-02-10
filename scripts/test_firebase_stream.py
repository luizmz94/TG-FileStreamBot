#!/usr/bin/env python3
"""
End-to-end tester for Firebase-authenticated direct streaming.

Flow:
1) Reads FIREBASE_TOKEN from environment.
2) Calls /auth/firebase/exchange and extracts stream_token.
3) Tests /direct/:message_id for a list of message IDs.
4) Streams by HTTP range chunks and prints per-chunk timing/throughput.

No external dependencies required (Python stdlib only).
"""

from __future__ import annotations

import argparse
import json
import math
import os
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from datetime import datetime
from typing import Dict, List, Optional, Tuple


DEFAULT_MESSAGE_IDS = [
    523915,
    523916,
    523917,
    523918,
    523919,
    523920,
    523921,
    523922,
    523923,
    523924,
    523925,
    523926,
]


def ts() -> str:
    return datetime.now().strftime("%H:%M:%S.%f")[:-3]


def log(level: str, message: str) -> None:
    print(f"{ts()} [{level}] {message}", flush=True)


def unquote_env_value(value: str) -> str:
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
        return value[1:-1]
    return value


def load_env_file(path: str, override: bool = False) -> int:
    loaded = 0
    with open(path, "r", encoding="utf-8") as f:
        for raw_line in f:
            line = raw_line.strip()
            if not line or line.startswith("#"):
                continue
            if line.startswith("export "):
                line = line[len("export ") :].strip()
            if "=" not in line:
                continue
            key, value = line.split("=", 1)
            key = key.strip()
            if not key:
                continue
            value = unquote_env_value(value)
            if override or key not in os.environ:
                os.environ[key] = value
                loaded += 1
    return loaded


def discover_default_env_file() -> Optional[str]:
    candidates: List[str] = []
    explicit = os.getenv("STREAM_TEST_ENV_FILE", "").strip()
    if explicit:
        candidates.append(explicit)

    script_dir = os.path.dirname(os.path.abspath(__file__))
    candidates.extend(
        [
            os.path.join(os.getcwd(), "fsb.env"),
            os.path.join(script_dir, "..", "fsb.env"),
            os.path.join(script_dir, "fsb.env"),
        ]
    )

    seen = set()
    for candidate in candidates:
        normalized = os.path.abspath(candidate)
        if normalized in seen:
            continue
        seen.add(normalized)
        if os.path.isfile(normalized):
            return normalized
    return None


@dataclass
class RequestMetrics:
    status: Optional[int]
    headers: Dict[str, str]
    body: bytes
    elapsed_s: float
    ttfb_s: Optional[float]
    error: Optional[str]


@dataclass
class ChunkResult:
    ok: bool
    status: Optional[int]
    start: int
    end: int
    bytes_read: int
    elapsed_s: float
    ttfb_s: Optional[float]
    throughput_mbps: float
    error: Optional[str]


def normalize_base_url(value: str) -> str:
    value = value.strip()
    if not value:
        return "http://localhost:8000"
    return value.rstrip("/")


def parse_message_ids(raw: str) -> List[int]:
    if not raw:
        return DEFAULT_MESSAGE_IDS
    parsed: List[int] = []
    for item in raw.split(","):
        item = item.strip()
        if not item:
            continue
        parsed.append(int(item))
    if not parsed:
        raise ValueError("message list is empty")
    return parsed


def parse_content_range_size(content_range: str) -> Optional[int]:
    # bytes start-end/total
    if not content_range:
        return None
    if "/" not in content_range:
        return None
    total = content_range.rsplit("/", 1)[1].strip()
    if total == "*":
        return None
    try:
        return int(total)
    except ValueError:
        return None


def parse_int_header(headers: Dict[str, str], name: str) -> Optional[int]:
    value = headers.get(name)
    if value is None:
        return None
    try:
        return int(value.strip())
    except ValueError:
        return None


def build_url(base_url: str, path: str, query: Optional[Dict[str, str]] = None) -> str:
    url = f"{base_url}{path}"
    if not query:
        return url
    encoded = urllib.parse.urlencode(query)
    return f"{url}?{encoded}"


def make_ssl_context(insecure: bool) -> Optional[ssl.SSLContext]:
    if not insecure:
        return None
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    return ctx


def request_http(
    method: str,
    url: str,
    headers: Optional[Dict[str, str]],
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
    read_body: bool = True,
) -> RequestMetrics:
    req = urllib.request.Request(url=url, method=method, headers=headers or {})
    start = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout_s, context=ssl_context) as resp:
            ttfb = time.perf_counter() - start
            status = resp.getcode()
            resp_headers = {k.lower(): v for k, v in resp.headers.items()}
            body = resp.read() if read_body else b""
            elapsed = time.perf_counter() - start
            return RequestMetrics(
                status=status,
                headers=resp_headers,
                body=body,
                elapsed_s=elapsed,
                ttfb_s=ttfb,
                error=None,
            )
    except urllib.error.HTTPError as e:
        ttfb = time.perf_counter() - start
        body = e.read() if read_body else b""
        elapsed = time.perf_counter() - start
        headers_map = {}
        if e.headers:
            headers_map = {k.lower(): v for k, v in e.headers.items()}
        return RequestMetrics(
            status=e.code,
            headers=headers_map,
            body=body,
            elapsed_s=elapsed,
            ttfb_s=ttfb,
            error=str(e),
        )
    except Exception as e:
        elapsed = time.perf_counter() - start
        return RequestMetrics(
            status=None,
            headers={},
            body=b"",
            elapsed_s=elapsed,
            ttfb_s=None,
            error=str(e),
        )


def stream_range_chunk(
    url: str,
    headers: Dict[str, str],
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
) -> ChunkResult:
    req = urllib.request.Request(url=url, method="GET", headers=headers)
    start = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout_s, context=ssl_context) as resp:
            ttfb = time.perf_counter() - start
            status = resp.getcode()
            bytes_read = 0
            while True:
                buf = resp.read(64 * 1024)
                if not buf:
                    break
                bytes_read += len(buf)
            elapsed = time.perf_counter() - start
            throughput_mbps = (bytes_read / (1024 * 1024)) / elapsed if elapsed > 0 else 0.0
            range_header = headers.get("Range", "bytes=0-0")
            start_s, end_s = range_header.replace("bytes=", "").split("-", 1)
            return ChunkResult(
                ok=(status in (200, 206)),
                status=status,
                start=int(start_s),
                end=int(end_s),
                bytes_read=bytes_read,
                elapsed_s=elapsed,
                ttfb_s=ttfb,
                throughput_mbps=throughput_mbps,
                error=None,
            )
    except urllib.error.HTTPError as e:
        elapsed = time.perf_counter() - start
        range_header = headers.get("Range", "bytes=0-0")
        start_s, end_s = range_header.replace("bytes=", "").split("-", 1)
        return ChunkResult(
            ok=False,
            status=e.code,
            start=int(start_s),
            end=int(end_s),
            bytes_read=0,
            elapsed_s=elapsed,
            ttfb_s=None,
            throughput_mbps=0.0,
            error=str(e),
        )
    except Exception as e:
        elapsed = time.perf_counter() - start
        range_header = headers.get("Range", "bytes=0-0")
        start_s, end_s = range_header.replace("bytes=", "").split("-", 1)
        return ChunkResult(
            ok=False,
            status=None,
            start=int(start_s),
            end=int(end_s),
            bytes_read=0,
            elapsed_s=elapsed,
            ttfb_s=None,
            throughput_mbps=0.0,
            error=str(e),
        )


def should_retry(chunk: ChunkResult) -> bool:
    if chunk.ok:
        return False
    if chunk.status is None:
        return True
    # Retry transient server/network errors.
    return chunk.status >= 500 or chunk.status in (408, 425, 429)


def exchange_stream_token(
    base_url: str,
    firebase_token: str,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
    exchange_path: str,
) -> Tuple[str, Optional[int], Dict[str, str]]:
    exchange_url = build_url(base_url, exchange_path)
    log("STEP", f"Exchange Firebase token -> {exchange_url}")
    headers = {"Authorization": f"Bearer {firebase_token}"}
    result = request_http(
        method="POST",
        url=exchange_url,
        headers=headers,
        timeout_s=timeout_s,
        ssl_context=ssl_context,
        read_body=True,
    )

    log(
        "HTTP",
        f"exchange status={result.status} elapsed={result.elapsed_s*1000:.1f}ms ttfb={((result.ttfb_s or 0)*1000):.1f}ms",
    )

    if result.status != 200:
        body_preview = result.body.decode("utf-8", errors="replace")[:500]
        hint = ""
        if result.status is None and result.error and "Connection refused" in result.error:
            hint = (
                " Hint: verify the server port with "
                "`lsof -nP -iTCP -sTCP:LISTEN | rg fsb` and match --base-url."
            )
        raise RuntimeError(
            f"exchange failed (status={result.status}, error={result.error}, body={body_preview}).{hint}"
        )

    try:
        payload = json.loads(result.body.decode("utf-8"))
    except Exception as e:
        raise RuntimeError(f"exchange response is not valid JSON: {e}") from e

    stream_token = payload.get("stream_token")
    expires_at = payload.get("expires_at")
    if not stream_token:
        raise RuntimeError("exchange JSON missing `stream_token`")

    exp_text = str(expires_at) if expires_at is not None else "n/a"
    log("OK", f"exchange success, got stream_token (len={len(stream_token)}), expires_at={exp_text}")

    return stream_token, expires_at, result.headers


def auth_headers_for_mode(auth_mode: str, stream_token: str) -> Dict[str, str]:
    if auth_mode == "query":
        return {}
    if auth_mode == "header":
        return {"x-stream-token": stream_token}
    if auth_mode == "bearer":
        return {"Authorization": f"Bearer {stream_token}"}
    raise ValueError(f"unknown auth mode: {auth_mode}")


def direct_url_for_mode(base_url: str, message_id: int, stream_token: str, auth_mode: str) -> str:
    path = f"/direct/{message_id}"
    if auth_mode == "query":
        return build_url(base_url, path, {"st": stream_token})
    return build_url(base_url, path)


def probe_message(
    base_url: str,
    message_id: int,
    stream_token: str,
    chunk_size: int,
    chunks_per_message: int,
    full: bool,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
    retries: int,
    auth_mode: str,
) -> bool:
    url = direct_url_for_mode(base_url, message_id, stream_token, auth_mode)
    base_headers = auth_headers_for_mode(auth_mode, stream_token)

    log("MSG", f"message_id={message_id} mode={auth_mode} url={url}")

    # HEAD probe
    head = request_http(
        method="HEAD",
        url=url,
        headers=base_headers,
        timeout_s=timeout_s,
        ssl_context=ssl_context,
        read_body=False,
    )
    log(
        "HEAD",
        f"status={head.status} elapsed={head.elapsed_s*1000:.1f}ms content-length={head.headers.get('content-length', 'n/a')} content-type={head.headers.get('content-type', 'n/a')}",
    )

    total_size = parse_int_header(head.headers, "content-length")
    if total_size is None:
        total_size = parse_content_range_size(head.headers.get("content-range", ""))

    # If HEAD is not useful, probe with first byte request.
    if total_size is None or (head.status is not None and head.status >= 400):
        probe_headers = dict(base_headers)
        probe_headers["Range"] = "bytes=0-0"
        probe = stream_range_chunk(url=url, headers=probe_headers, timeout_s=timeout_s, ssl_context=ssl_context)
        log(
            "PROBE",
            f"status={probe.status} elapsed={probe.elapsed_s*1000:.1f}ms bytes={probe.bytes_read} err={probe.error or 'none'}",
        )
        # Try to resolve total from probe via GET headers (Content-Range is not available in stream_range_chunk return).
        # Run a light GET again just for header inspection.
        probe_header_res = request_http(
            method="GET",
            url=url,
            headers=probe_headers,
            timeout_s=timeout_s,
            ssl_context=ssl_context,
            read_body=False,
        )
        if total_size is None:
            total_size = parse_content_range_size(probe_header_res.headers.get("content-range", ""))

    if total_size is not None:
        log("INFO", f"message_id={message_id} total_size={total_size} bytes")
    else:
        log("WARN", f"message_id={message_id} could not determine total size, using fixed chunk count")

    if full and total_size is not None:
        target_chunks = int(math.ceil(total_size / chunk_size))
    else:
        if total_size is not None:
            max_chunks = int(math.ceil(total_size / chunk_size))
            target_chunks = min(chunks_per_message, max_chunks)
        else:
            target_chunks = chunks_per_message

    if target_chunks <= 0:
        log("WARN", f"message_id={message_id} no chunks to test")
        return False

    chunk_ok = 0
    total_bytes = 0
    total_elapsed = 0.0

    for idx in range(target_chunks):
        start = idx * chunk_size
        if total_size is not None:
            end = min(total_size - 1, start + chunk_size - 1)
            if start > end:
                break
        else:
            end = start + chunk_size - 1

        headers = dict(base_headers)
        headers["Range"] = f"bytes={start}-{end}"

        attempt = 0
        last: Optional[ChunkResult] = None
        while attempt <= retries:
            result = stream_range_chunk(url=url, headers=headers, timeout_s=timeout_s, ssl_context=ssl_context)
            last = result
            if result.ok or not should_retry(result) or attempt == retries:
                break
            sleep_s = 0.2 * (attempt + 1)
            log(
                "RETRY",
                f"message_id={message_id} chunk={idx+1}/{target_chunks} attempt={attempt+1} status={result.status} err={result.error} waiting={sleep_s:.1f}s",
            )
            time.sleep(sleep_s)
            attempt += 1

        assert last is not None
        expected = end - start + 1
        length_ok = last.bytes_read == expected or (last.status == 200 and last.bytes_read <= expected)
        ok = last.ok and length_ok

        status_text = f"status={last.status}" if last.status is not None else "status=n/a"
        ttfb_ms = (last.ttfb_s * 1000) if last.ttfb_s is not None else 0.0
        log(
            "CHUNK",
            (
                f"msg={message_id} #{idx+1}/{target_chunks} range={start}-{end} "
                f"{status_text} bytes={last.bytes_read}/{expected} "
                f"elapsed={last.elapsed_s*1000:.1f}ms ttfb={ttfb_ms:.1f}ms "
                f"rate={last.throughput_mbps:.2f} MB/s ok={ok} "
                f"err={last.error or 'none'}"
            ),
        )

        if ok:
            chunk_ok += 1
            total_bytes += last.bytes_read
            total_elapsed += last.elapsed_s

    if chunk_ok == 0:
        log("FAIL", f"message_id={message_id} all chunks failed")
        return False

    avg_rate = (total_bytes / (1024 * 1024)) / total_elapsed if total_elapsed > 0 else 0.0
    log(
        "DONE",
        f"message_id={message_id} chunks_ok={chunk_ok}/{target_chunks} bytes={total_bytes} avg_rate={avg_rate:.2f} MB/s total_time={total_elapsed:.2f}s",
    )
    return chunk_ok == target_chunks


def main() -> int:
    default_env_file = discover_default_env_file()

    parser = argparse.ArgumentParser(
        description="Test Firebase exchange + direct streaming with detailed chunk metrics."
    )
    parser.add_argument(
        "--env-file",
        default=default_env_file,
        help="Optional env file to preload (default: auto-discover fsb.env)",
    )
    parser.add_argument(
        "--base-url",
        default=None,
        help="Base URL of stream server (default: STREAM_BASE_URL, HOST, or http://localhost:8000)",
    )
    parser.add_argument(
        "--messages",
        default=",".join(str(x) for x in DEFAULT_MESSAGE_IDS),
        help="Comma-separated message IDs",
    )
    parser.add_argument(
        "--chunk-size",
        type=int,
        default=1024 * 1024,
        help="Chunk size in bytes for range requests (default: 1048576)",
    )
    parser.add_argument(
        "--chunks-per-message",
        type=int,
        default=3,
        help="How many chunks to test per message when --full is not set (default: 3)",
    )
    parser.add_argument(
        "--full",
        action="store_true",
        help="Download all chunks for each message (can be slow for large files)",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=30.0,
        help="HTTP timeout in seconds (default: 30)",
    )
    parser.add_argument(
        "--retries",
        type=int,
        default=2,
        help="Retry count for transient chunk failures (default: 2)",
    )
    parser.add_argument(
        "--auth-mode",
        choices=["query", "header", "bearer"],
        default="query",
        help="How to send stream_token to /direct (default: query)",
    )
    parser.add_argument(
        "--exchange-path",
        default="/auth/firebase/exchange",
        help="Exchange endpoint path (default: /auth/firebase/exchange)",
    )
    parser.add_argument(
        "--insecure",
        action="store_true",
        help="Disable TLS certificate verification (for local/self-signed HTTPS)",
    )

    args = parser.parse_args()

    if args.env_file:
        env_file_path = os.path.abspath(args.env_file)
        if os.path.isfile(env_file_path):
            loaded = load_env_file(env_file_path, override=False)
            log("INFO", f"loaded {loaded} vars from env file: {env_file_path}")
        else:
            log("WARN", f"env file not found: {env_file_path}")

    resolved_base_url = args.base_url or os.getenv("STREAM_BASE_URL") or os.getenv("HOST") or "http://localhost:8000"
    base_url = normalize_base_url(resolved_base_url)
    messages = parse_message_ids(args.messages)
    firebase_token = os.getenv("FIREBASE_TOKEN", "").strip()
    if not firebase_token:
        log("ERROR", "FIREBASE_TOKEN not found in environment.")
        log("INFO", "export FIREBASE_TOKEN='eyJhbGciOiJSUzI1NiIs...'\n")
        return 2

    if args.chunk_size <= 0:
        log("ERROR", "--chunk-size must be > 0")
        return 2
    if args.chunks_per_message <= 0 and not args.full:
        log("ERROR", "--chunks-per-message must be > 0 when --full is not set")
        return 2
    if args.retries < 0:
        log("ERROR", "--retries must be >= 0")
        return 2

    ssl_context = make_ssl_context(args.insecure)

    log("INFO", f"base_url={base_url}")
    log("INFO", f"messages={messages}")
    log(
        "INFO",
        (
            f"chunk_size={args.chunk_size} bytes, chunks_per_message={args.chunks_per_message}, "
            f"full={args.full}, timeout={args.timeout}s, retries={args.retries}, auth_mode={args.auth_mode}"
        ),
    )

    try:
        stream_token, expires_at, _ = exchange_stream_token(
            base_url=base_url,
            firebase_token=firebase_token,
            timeout_s=args.timeout,
            ssl_context=ssl_context,
            exchange_path=args.exchange_path,
        )
        if expires_at:
            now = int(time.time())
            remaining = max(0, int(expires_at) - now)
            log("INFO", f"stream_token remaining_ttl={remaining}s")
    except Exception as e:
        log("ERROR", str(e))
        return 1

    success = 0
    failed = 0
    start_all = time.perf_counter()
    for message_id in messages:
        ok = probe_message(
            base_url=base_url,
            message_id=message_id,
            stream_token=stream_token,
            chunk_size=args.chunk_size,
            chunks_per_message=args.chunks_per_message,
            full=args.full,
            timeout_s=args.timeout,
            ssl_context=ssl_context,
            retries=args.retries,
            auth_mode=args.auth_mode,
        )
        if ok:
            success += 1
        else:
            failed += 1

    total_elapsed = time.perf_counter() - start_all
    log("SUMMARY", f"messages_ok={success} messages_failed={failed} total_time={total_elapsed:.2f}s")

    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
