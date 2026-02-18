#!/usr/bin/env python3
"""
Concurrent performance tester for TG-FileStreamBot /direct streaming.

Flow:
1) Asks email & password interactively, authenticates via Firebase REST API.
2) Exchanges the Firebase ID token for a stream session token.
3) Fires concurrent range-request downloads for multiple media IDs.
4) Collects per-request metrics: TTFB, throughput, worker assignment, errors.
5) Prints a summary table with performance analysis per media and overall.

Usage:
    python scripts/test_concurrent_performance.py

Dependencies: Python 3.8+ stdlib only (no pip install needed).
"""

from __future__ import annotations

import argparse
import getpass
import json
import math
import os
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from datetime import datetime
from threading import Lock
from typing import Dict, List, Optional, Tuple

# ---------------------------------------------------------------------------
# Media IDs extracted from production logs
# ---------------------------------------------------------------------------
DEFAULT_MESSAGE_IDS = [
    # Batch 1 – from production logs (larger files, ~700MB–1.3GB)
    479688, 479689, 479691, 479686, 479693, 479695, 479697,
    # Batch 2 – additional media (439960–439975)
    439960, 439961, 439962, 439963, 439964, 439965, 439966,
    439967, 439968, 439969, 439970, 439971, 439972, 439973,
    439974, 439975,
]

# Firebase Web API key is intentionally not hardcoded.
# Provide via --firebase-api-key, FIREBASE_API_KEY env var, or interactive prompt.

# Default URL shown at execution time (user can override interactively).
DEFAULT_BASE_URL = "https://streamer.mediatg.com"

# Fixed benchmark profile (intentionally hardcoded for comparable runs).
FIXED_CHUNK_SIZE = 1 * 1024 * 1024
FIXED_NUM_CHUNKS = 5
FIXED_CONCURRENCY = 12
FIXED_SAME_MEDIA_REQUESTS = 10
FIXED_TIMEOUT_SECONDS = 120.0
FIXED_ROUNDS = 3
FIXED_TESTS = ["sequential", "burst", "multi_chunk", "same_media", "ramp_up"]

# Where result files are stored by default.
DEFAULT_RESULTS_DIR = "scripts"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_print_lock = Lock()


def ts() -> str:
    return datetime.now().strftime("%H:%M:%S.%f")[:-3]


def log(level: str, message: str) -> None:
    with _print_lock:
        print(f"{ts()} [{level:>7s}] {message}", flush=True)


def hr(char: str = "─", width: int = 100) -> str:
    return char * width


def sanitize_filename_part(value: str) -> str:
    out = []
    for ch in value.lower():
        if ch.isalnum():
            out.append(ch)
        else:
            out.append("-")
    cleaned = "".join(out).strip("-")
    while "--" in cleaned:
        cleaned = cleaned.replace("--", "-")
    return cleaned or "unknown"


def prompt_base_url(default_url: str) -> str:
    prompt = f"Base URL [{default_url}]: "
    typed = input(prompt).strip()
    base_url = typed or default_url
    return base_url.rstrip("/")


def make_results_path(base_url: str, results_dir: str = DEFAULT_RESULTS_DIR) -> str:
    parsed = urllib.parse.urlparse(base_url)
    host = parsed.netloc or parsed.path or "unknown"
    host_tag = sanitize_filename_part(host)
    timeout_tag = int(FIXED_TIMEOUT_SECONDS)

    prefix = (
        f"baseline_{host_tag}"
        f"_cs{FIXED_CHUNK_SIZE}"
        f"_c{FIXED_CONCURRENCY}"
        f"_r{FIXED_ROUNDS}"
        f"_nc{FIXED_NUM_CHUNKS}"
        f"_sm{FIXED_SAME_MEDIA_REQUESTS}"
        f"_t{timeout_tag}"
    )

    os.makedirs(results_dir, exist_ok=True)
    max_seq = 0
    for name in os.listdir(results_dir):
        if not name.startswith(prefix + "_seq") or not name.endswith(".json"):
            continue
        seq_str = name[len(prefix) + 4 : -5]
        try:
            seq = int(seq_str)
            max_seq = max(max_seq, seq)
        except ValueError:
            continue

    next_seq = max_seq + 1
    return os.path.join(results_dir, f"{prefix}_seq{next_seq:03d}.json")


# ---------------------------------------------------------------------------
# Env file loader (reuse from existing script)
# ---------------------------------------------------------------------------

def unquote_env_value(value: str) -> str:
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
        return value[1:-1]
    return value


def load_env_file(path: str) -> int:
    loaded = 0
    try:
        with open(path, "r", encoding="utf-8") as f:
            for raw_line in f:
                line = raw_line.strip()
                if not line or line.startswith("#"):
                    continue
                if line.startswith("export "):
                    line = line[len("export "):].strip()
                if "=" not in line:
                    continue
                key, value = line.split("=", 1)
                key = key.strip()
                if not key:
                    continue
                value = unquote_env_value(value)
                if key not in os.environ:
                    os.environ[key] = value
                    loaded += 1
    except FileNotFoundError:
        pass
    return loaded


def discover_env_file() -> Optional[str]:
    script_dir = os.path.dirname(os.path.abspath(__file__))
    candidates = [
        os.path.join(os.getcwd(), "fsb.env"),
        os.path.join(script_dir, "..", "fsb.env"),
    ]
    for c in candidates:
        p = os.path.abspath(c)
        if os.path.isfile(p):
            return p
    return None


# ---------------------------------------------------------------------------
# SSL helper
# ---------------------------------------------------------------------------

def make_ssl_context(insecure: bool) -> Optional[ssl.SSLContext]:
    if not insecure:
        return None
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    return ctx


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------

def http_post_json(
    url: str,
    payload: dict,
    timeout_s: float = 15.0,
    ssl_context: Optional[ssl.SSLContext] = None,
) -> Tuple[int, dict]:
    """POST JSON and return (status_code, response_dict)."""
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        url=url,
        data=data,
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout_s, context=ssl_context) as resp:
            body = json.loads(resp.read().decode("utf-8"))
            return resp.getcode(), body
    except urllib.error.HTTPError as e:
        body_bytes = e.read()
        try:
            body = json.loads(body_bytes.decode("utf-8"))
        except Exception:
            body = {"raw": body_bytes.decode("utf-8", errors="replace")[:500]}
        return e.code, body


def http_request(
    url: str,
    headers: Dict[str, str],
    timeout_s: float = 60.0,
    ssl_context: Optional[ssl.SSLContext] = None,
    method: str = "GET",
) -> "RequestResult":
    """Generic HTTP request that streams the body and measures timings."""
    req = urllib.request.Request(url=url, method=method, headers=headers)
    start = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout_s, context=ssl_context) as resp:
            ttfb = time.perf_counter() - start
            status = resp.getcode()
            resp_headers = {k.lower(): v for k, v in resp.headers.items()}
            bytes_read = 0
            while True:
                buf = resp.read(128 * 1024)
                if not buf:
                    break
                bytes_read += len(buf)
            elapsed = time.perf_counter() - start
            return RequestResult(
                status=status,
                headers=resp_headers,
                bytes_read=bytes_read,
                elapsed_s=elapsed,
                ttfb_s=ttfb,
                error=None,
            )
    except urllib.error.HTTPError as e:
        ttfb = time.perf_counter() - start
        resp_headers = {k.lower(): v for k, v in e.headers.items()} if e.headers else {}
        try:
            _ = e.read()
        except Exception:
            pass
        elapsed = time.perf_counter() - start
        return RequestResult(
            status=e.code,
            headers=resp_headers,
            bytes_read=0,
            elapsed_s=elapsed,
            ttfb_s=ttfb,
            error=str(e),
        )
    except Exception as e:
        elapsed = time.perf_counter() - start
        return RequestResult(
            status=None,
            headers={},
            bytes_read=0,
            elapsed_s=elapsed,
            ttfb_s=None,
            error=str(e),
        )


@dataclass
class RequestResult:
    status: Optional[int]
    headers: Dict[str, str]
    bytes_read: int
    elapsed_s: float
    ttfb_s: Optional[float]
    error: Optional[str]


# ---------------------------------------------------------------------------
# Firebase email/password login
# ---------------------------------------------------------------------------

def firebase_sign_in(api_key: str, email: str, password: str) -> str:
    """
    Authenticate with Firebase using email/password via the REST API.
    Returns the Firebase ID token.
    """
    url = f"https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key={api_key}"
    payload = {
        "email": email,
        "password": password,
        "returnSecureToken": True,
    }
    log("AUTH", f"Signing in to Firebase as {email}...")
    status, body = http_post_json(url, payload)

    if status != 200:
        error_msg = body.get("error", {}).get("message", "unknown error") if isinstance(body, dict) else str(body)
        raise RuntimeError(f"Firebase sign-in failed (HTTP {status}): {error_msg}")

    id_token = body.get("idToken")
    if not id_token:
        raise RuntimeError("Firebase response missing 'idToken'")

    log("AUTH", f"Firebase sign-in OK (uid={body.get('localId', '?')}, email={body.get('email', '?')})")
    return id_token


# ---------------------------------------------------------------------------
# Stream token exchange
# ---------------------------------------------------------------------------

def exchange_stream_token(
    base_url: str,
    firebase_token: str,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
) -> Tuple[str, Optional[int]]:
    """Exchange Firebase ID token for a stream session token."""
    url = f"{base_url}/auth/firebase/exchange"
    log("EXCH", f"Exchanging Firebase token at {url}")

    req = urllib.request.Request(
        url=url,
        method="GET",
        headers={"Authorization": f"Bearer {firebase_token}"},
    )
    start = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout_s, context=ssl_context) as resp:
            elapsed = time.perf_counter() - start
            body = json.loads(resp.read().decode("utf-8"))
            stream_token = body.get("stream_token")
            expires_at = body.get("expires_at")
            if not stream_token:
                raise RuntimeError("Exchange response missing 'stream_token'")
            log("EXCH", f"OK in {elapsed*1000:.0f}ms – token len={len(stream_token)}, expires_at={expires_at}")
            return stream_token, expires_at
    except urllib.error.HTTPError as e:
        elapsed = time.perf_counter() - start
        error_body = e.read().decode("utf-8", errors="replace")[:300]
        raise RuntimeError(f"Exchange failed (HTTP {e.code}) in {elapsed*1000:.0f}ms: {error_body}") from e


# ---------------------------------------------------------------------------
# Streaming test data structures
# ---------------------------------------------------------------------------

@dataclass
class StreamTestResult:
    message_id: int
    test_label: str
    range_spec: str
    status: Optional[int]
    bytes_read: int
    expected_bytes: int
    elapsed_s: float
    ttfb_s: Optional[float]
    throughput_mbps: float
    content_range: str
    error: Optional[str]
    worker_info: str  # parsed from response headers if available


@dataclass
class MediaInfo:
    message_id: int
    total_size: Optional[int] = None
    content_type: Optional[str] = None


# ---------------------------------------------------------------------------
# Core streaming tests
# ---------------------------------------------------------------------------

def probe_media_size(
    base_url: str,
    message_id: int,
    stream_token: str,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
) -> MediaInfo:
    """Discover media total size via a bytes=0-1 range request."""
    url = f"{base_url}/direct/{message_id}?st={stream_token}"
    headers = {"Range": "bytes=0-1"}
    result = http_request(url, headers, timeout_s=timeout_s, ssl_context=ssl_context)

    total_size = None
    content_range = result.headers.get("content-range", "")
    if "/" in content_range:
        total_str = content_range.rsplit("/", 1)[1].strip()
        if total_str != "*":
            try:
                total_size = int(total_str)
            except ValueError:
                pass

    content_type = result.headers.get("content-type", "unknown")
    return MediaInfo(
        message_id=message_id,
        total_size=total_size,
        content_type=content_type,
    )


def stream_range(
    base_url: str,
    message_id: int,
    stream_token: str,
    range_start: int,
    range_end: int,
    test_label: str,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
) -> StreamTestResult:
    """Download a specific byte range and measure performance."""
    url = f"{base_url}/direct/{message_id}?st={stream_token}"
    range_spec = f"bytes={range_start}-{range_end}"
    headers = {"Range": range_spec}
    expected = range_end - range_start + 1

    result = http_request(url, headers, timeout_s=timeout_s, ssl_context=ssl_context)

    throughput = 0.0
    if result.elapsed_s > 0 and result.bytes_read > 0:
        throughput = (result.bytes_read / (1024 * 1024)) / result.elapsed_s

    content_range = result.headers.get("content-range", "")

    return StreamTestResult(
        message_id=message_id,
        test_label=test_label,
        range_spec=range_spec,
        status=result.status,
        bytes_read=result.bytes_read,
        expected_bytes=expected,
        elapsed_s=result.elapsed_s,
        ttfb_s=result.ttfb_s,
        throughput_mbps=throughput,
        content_range=content_range,
        error=result.error,
        worker_info="",
    )


# ---------------------------------------------------------------------------
# Test scenarios
# ---------------------------------------------------------------------------

def run_sequential_test(
    base_url: str,
    message_ids: List[int],
    stream_token: str,
    chunk_size: int,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
) -> List[StreamTestResult]:
    """Sequentially request the first chunk of each media."""
    results = []
    for mid in message_ids:
        r = stream_range(
            base_url, mid, stream_token,
            0, chunk_size - 1,
            test_label="sequential",
            timeout_s=timeout_s,
            ssl_context=ssl_context,
        )
        log_result(r)
        results.append(r)
    return results


def run_concurrent_burst(
    base_url: str,
    message_ids: List[int],
    stream_token: str,
    chunk_size: int,
    concurrency: int,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
    label: str = "burst",
) -> List[StreamTestResult]:
    """Fire all message requests concurrently."""
    results: List[StreamTestResult] = []

    def fetch(mid: int) -> StreamTestResult:
        return stream_range(
            base_url, mid, stream_token,
            0, chunk_size - 1,
            test_label=label,
            timeout_s=timeout_s,
            ssl_context=ssl_context,
        )

    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = {pool.submit(fetch, mid): mid for mid in message_ids}
        for future in as_completed(futures):
            r = future.result()
            log_result(r)
            results.append(r)

    return results


def run_concurrent_multi_chunk(
    base_url: str,
    message_ids: List[int],
    stream_token: str,
    chunk_size: int,
    num_chunks: int,
    concurrency: int,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
    media_infos: Dict[int, MediaInfo],
) -> List[StreamTestResult]:
    """
    For each media, request N consecutive chunks concurrently across all media.
    Simulates a player that seeks around multiple videos.
    """
    tasks: List[Tuple[int, int, int, str]] = []  # (mid, start, end, label)

    for mid in message_ids:
        info = media_infos.get(mid)
        total = info.total_size if info else None
        for i in range(num_chunks):
            start = i * chunk_size
            end = start + chunk_size - 1
            if total is not None:
                end = min(end, total - 1)
                if start >= total:
                    break
            tasks.append((mid, start, end, f"multi_chunk_{i}"))

    results: List[StreamTestResult] = []

    def fetch(task: Tuple[int, int, int, str]) -> StreamTestResult:
        mid, start, end, label = task
        return stream_range(
            base_url, mid, stream_token,
            start, end,
            test_label=label,
            timeout_s=timeout_s,
            ssl_context=ssl_context,
        )

    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = {pool.submit(fetch, t): t for t in tasks}
        for future in as_completed(futures):
            r = future.result()
            log_result(r)
            results.append(r)

    return results


def run_same_media_concurrent(
    base_url: str,
    message_id: int,
    stream_token: str,
    chunk_size: int,
    num_requests: int,
    concurrency: int,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
    total_size: Optional[int],
) -> List[StreamTestResult]:
    """
    Hammer a single media with multiple concurrent requests at different offsets.
    Simulates what happens when a player quickly seeks around in the same video.
    """
    tasks: List[Tuple[int, int, str]] = []
    if total_size and total_size > chunk_size:
        # Spread requests across the file
        step = total_size // num_requests
        for i in range(num_requests):
            start = i * step
            end = min(start + chunk_size - 1, total_size - 1)
            tasks.append((start, end, f"same_media_{i}"))
    else:
        for i in range(num_requests):
            start = i * chunk_size
            end = start + chunk_size - 1
            tasks.append((start, end, f"same_media_{i}"))

    results: List[StreamTestResult] = []

    def fetch(task: Tuple[int, int, str]) -> StreamTestResult:
        start, end, label = task
        return stream_range(
            base_url, message_id, stream_token,
            start, end,
            test_label=label,
            timeout_s=timeout_s,
            ssl_context=ssl_context,
        )

    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = {pool.submit(fetch, t): t for t in tasks}
        for future in as_completed(futures):
            r = future.result()
            log_result(r)
            results.append(r)

    return results


def run_ramp_up(
    base_url: str,
    message_ids: List[int],
    stream_token: str,
    chunk_size: int,
    max_concurrency: int,
    timeout_s: float,
    ssl_context: Optional[ssl.SSLContext],
) -> List[StreamTestResult]:
    """
    Gradually increase concurrency from 1 to max_concurrency.
    For each level, fire that many requests from the message list.
    Reveals the point where performance degrades.
    """
    results: List[StreamTestResult] = []
    levels = [1, 2, 4, 8, 12, 16, 20, max_concurrency]
    levels = sorted(set(l for l in levels if l <= max_concurrency))
    if max_concurrency not in levels:
        levels.append(max_concurrency)

    for conc in levels:
        # Pick conc messages (cycle if needed)
        selected = []
        for i in range(conc):
            selected.append(message_ids[i % len(message_ids)])

        label = f"ramp_c{conc}"
        log("RAMP", f"concurrency={conc} → {len(selected)} requests")

        batch_start = time.perf_counter()

        def fetch(mid: int, lbl: str = label) -> StreamTestResult:
            return stream_range(
                base_url, mid, stream_token,
                0, chunk_size - 1,
                test_label=lbl,
                timeout_s=timeout_s,
                ssl_context=ssl_context,
            )

        with ThreadPoolExecutor(max_workers=conc) as pool:
            futures = {pool.submit(fetch, mid): mid for mid in selected}
            batch_results = []
            for future in as_completed(futures):
                r = future.result()
                log_result(r)
                batch_results.append(r)

        batch_elapsed = time.perf_counter() - batch_start
        ok_results = [r for r in batch_results if r.status in (200, 206) and r.error is None]
        if ok_results:
            avg_ttfb = sum(r.ttfb_s for r in ok_results if r.ttfb_s) / len(ok_results) * 1000
            avg_rate = sum(r.throughput_mbps for r in ok_results) / len(ok_results)
            log(
                "RAMP",
                f"c={conc:>2d} → ok={len(ok_results)}/{len(batch_results)} "
                f"avg_ttfb={avg_ttfb:.0f}ms  avg_rate={avg_rate:.2f} MB/s  "
                f"wall={batch_elapsed:.1f}s",
            )
        results.extend(batch_results)
        time.sleep(0.5)  # brief cooldown between levels

    return results


# ---------------------------------------------------------------------------
# Logging & reporting
# ---------------------------------------------------------------------------

def log_result(r: StreamTestResult) -> None:
    status = r.status or "ERR"
    ttfb_ms = r.ttfb_s * 1000 if r.ttfb_s else 0
    ok = "✓" if r.status in (200, 206) and r.error is None else "✗"
    error_text = f" err={r.error}" if r.error else ""
    log(
        "REQ",
        f"{ok} msg={r.message_id} [{r.test_label:>15s}] {r.range_spec:<25s} "
        f"status={status} bytes={r.bytes_read:>12,}/{r.expected_bytes:>12,} "
        f"ttfb={ttfb_ms:>7.0f}ms elapsed={r.elapsed_s*1000:>8.0f}ms "
        f"rate={r.throughput_mbps:>7.2f} MB/s{error_text}",
    )


def format_bytes(n: int) -> str:
    if n >= 1_073_741_824:
        return f"{n/1_073_741_824:.2f} GB"
    if n >= 1_048_576:
        return f"{n/1_048_576:.2f} MB"
    if n >= 1024:
        return f"{n/1024:.2f} KB"
    return f"{n} B"


def print_summary(all_results: List[StreamTestResult], media_infos: Dict[int, MediaInfo]) -> None:
    print(f"\n{hr('═')}")
    print("  PERFORMANCE SUMMARY")
    print(hr("═"))

    # Group by test label
    by_label: Dict[str, List[StreamTestResult]] = {}
    for r in all_results:
        by_label.setdefault(r.test_label, []).append(r)

    for label, results in sorted(by_label.items()):
        ok_results = [r for r in results if r.status in (200, 206) and r.error is None]
        failed = len(results) - len(ok_results)
        if not ok_results:
            print(f"\n  [{label}] All {len(results)} requests FAILED")
            continue

        ttfbs = [r.ttfb_s * 1000 for r in ok_results if r.ttfb_s is not None]
        elapsed = [r.elapsed_s * 1000 for r in ok_results]
        rates = [r.throughput_mbps for r in ok_results]
        total_bytes = sum(r.bytes_read for r in ok_results)

        print(f"\n  [{label}] {len(ok_results)} OK / {failed} FAILED")
        print(f"  {'':>4s} Total bytes: {format_bytes(total_bytes)}")
        if ttfbs:
            print(
                f"  {'':>4s} TTFB   → min={min(ttfbs):>7.0f}ms  avg={sum(ttfbs)/len(ttfbs):>7.0f}ms  "
                f"max={max(ttfbs):>7.0f}ms  p50={sorted(ttfbs)[len(ttfbs)//2]:>7.0f}ms"
            )
        if elapsed:
            print(
                f"  {'':>4s} Total  → min={min(elapsed):>7.0f}ms  avg={sum(elapsed)/len(elapsed):>7.0f}ms  "
                f"max={max(elapsed):>7.0f}ms  p50={sorted(elapsed)[len(elapsed)//2]:>7.0f}ms"
            )
        if rates:
            print(
                f"  {'':>4s} Rate   → min={min(rates):>7.2f}     avg={sum(rates)/len(rates):>7.2f}     "
                f"max={max(rates):>7.2f}     p50={sorted(rates)[len(rates)//2]:>7.2f} MB/s"
            )

    # Group by message_id
    print(f"\n{hr('─')}")
    print("  PER-MEDIA BREAKDOWN")
    print(hr("─"))

    by_media: Dict[int, List[StreamTestResult]] = {}
    for r in all_results:
        by_media.setdefault(r.message_id, []).append(r)

    for mid in sorted(by_media.keys()):
        results = by_media[mid]
        info = media_infos.get(mid)
        size_text = format_bytes(info.total_size) if info and info.total_size else "unknown"
        ctype = info.content_type if info and info.content_type else "?"

        ok_results = [r for r in results if r.status in (200, 206) and r.error is None]
        failed = len(results) - len(ok_results)

        print(f"\n  message_id={mid}  size={size_text}  type={ctype}")
        print(f"  {'':>4s} requests={len(results)}  ok={len(ok_results)}  failed={failed}")

        if ok_results:
            ttfbs = [r.ttfb_s * 1000 for r in ok_results if r.ttfb_s is not None]
            rates = [r.throughput_mbps for r in ok_results]
            elapsed = [r.elapsed_s * 1000 for r in ok_results]
            if ttfbs:
                print(
                    f"  {'':>4s} TTFB   → min={min(ttfbs):>7.0f}ms  avg={sum(ttfbs)/len(ttfbs):>7.0f}ms  "
                    f"max={max(ttfbs):>7.0f}ms"
                )
            if elapsed:
                print(
                    f"  {'':>4s} Total  → min={min(elapsed):>7.0f}ms  avg={sum(elapsed)/len(elapsed):>7.0f}ms  "
                    f"max={max(elapsed):>7.0f}ms"
                )
            if rates:
                print(
                    f"  {'':>4s} Rate   → min={min(rates):>7.2f}     avg={sum(rates)/len(rates):>7.2f}     "
                    f"max={max(rates):>7.2f} MB/s"
                )

    # Overall
    print(f"\n{hr('─')}")
    all_ok = [r for r in all_results if r.status in (200, 206) and r.error is None]
    all_failed = len(all_results) - len(all_ok)
    print(
        f"  OVERALL: {len(all_ok)} OK / {all_failed} FAILED / {len(all_results)} total requests"
    )
    if all_ok:
        all_ttfbs = [r.ttfb_s * 1000 for r in all_ok if r.ttfb_s is not None]
        all_rates = [r.throughput_mbps for r in all_ok]
        if all_ttfbs:
            print(
                f"  {'':>4s} TTFB   → min={min(all_ttfbs):>7.0f}ms  avg={sum(all_ttfbs)/len(all_ttfbs):>7.0f}ms  "
                f"max={max(all_ttfbs):>7.0f}ms  p95={sorted(all_ttfbs)[int(len(all_ttfbs)*0.95)]:>7.0f}ms"
            )
        if all_rates:
            print(
                f"  {'':>4s} Rate   → min={min(all_rates):>7.2f}     avg={sum(all_rates)/len(all_rates):>7.2f}     "
                f"max={max(all_rates):>7.2f}     p50={sorted(all_rates)[len(all_rates)//2]:>7.2f} MB/s"
            )
    print(hr("═"))


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(
        description="Concurrent performance tester for TG-FileStreamBot streaming.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python scripts/test_concurrent_performance.py
  python scripts/test_concurrent_performance.py --base-url http://localhost:8000
  python scripts/test_concurrent_performance.py --messages 479688,479689
        """,
    )

    parser.add_argument(
        "--base-url",
        default=None,
        help="Optional default base URL used as prefilled value in the prompt.",
    )
    parser.add_argument(
        "--firebase-api-key",
        default=None,
        help="Firebase Web API key. Falls back to FIREBASE_API_KEY env var.",
    )
    parser.add_argument(
        "--email",
        default=None,
        help="Firebase email (will prompt interactively if not provided).",
    )
    parser.add_argument(
        "--messages",
        default=",".join(str(m) for m in DEFAULT_MESSAGE_IDS),
        help=f"Comma-separated message IDs (default: {','.join(str(m) for m in DEFAULT_MESSAGE_IDS)})",
    )
    parser.add_argument(
        "--same-media-id",
        type=int,
        default=None,
        help="Specific message_id for same_media test (default: first in --messages list)",
    )
    parser.add_argument(
        "--insecure",
        action="store_true",
        help="Disable TLS certificate verification",
    )

    args = parser.parse_args()

    # Load env file
    env_file = discover_env_file()
    if env_file:
        loaded = load_env_file(env_file)
        log("INFO", f"Loaded {loaded} vars from {env_file}")

    # Resolve URL default and ask interactively every run
    default_base_url = (args.base_url or DEFAULT_BASE_URL).rstrip("/")
    if not default_base_url:
        default_base_url = DEFAULT_BASE_URL

    print(f"\n{hr('═')}")
    print("  TARGET SERVER")
    print(hr("═"))
    base_url = prompt_base_url(default_base_url)
    if not base_url:
        log("ERROR", "Base URL is required.")
        return 2
    if "://" not in base_url:
        base_url = "http://" + base_url
    log("INFO", f"Target URL: {base_url}")

    # Resolve Firebase API key
    firebase_api_key = args.firebase_api_key or os.getenv("FIREBASE_API_KEY")
    if not firebase_api_key:
        firebase_api_key = input("Firebase Web API Key: ").strip()
    if not firebase_api_key:
        log("ERROR", "Firebase API key is required.")
        return 2

    # Parse message IDs
    message_ids = [int(x.strip()) for x in args.messages.split(",") if x.strip()]
    if not message_ids:
        log("ERROR", "No message IDs provided.")
        return 2

    # Fixed benchmark profile (kept constant to preserve comparability)
    chunk_size = FIXED_CHUNK_SIZE
    num_chunks = FIXED_NUM_CHUNKS
    concurrency = FIXED_CONCURRENCY
    same_media_requests = FIXED_SAME_MEDIA_REQUESTS
    timeout_s = FIXED_TIMEOUT_SECONDS
    rounds = FIXED_ROUNDS
    test_names = FIXED_TESTS[:]
    same_media_target_id = args.same_media_id or message_ids[0]

    ssl_context = make_ssl_context(args.insecure)

    print(f"\n{hr('═')}")
    print("  FIXED TEST PROFILE")
    print(hr("═"))
    print(f"  chunk_size={chunk_size} bytes")
    print(f"  concurrency={concurrency}")
    print(f"  rounds={rounds}")
    print(f"  num_chunks={num_chunks}")
    print(f"  same_media_requests={same_media_requests}")
    print(f"  timeout={timeout_s:.0f}s")
    print(f"  tests={','.join(test_names)}")

    # ── Firebase authentication ──
    print(f"\n{hr('═')}")
    print("  FIREBASE AUTHENTICATION")
    print(hr("═"))

    email = args.email
    if not email:
        email = input("Firebase Email: ").strip()
    if not email:
        log("ERROR", "Email is required.")
        return 2

    password = getpass.getpass("Firebase Password: ")
    if not password:
        log("ERROR", "Password is required.")
        return 2

    try:
        firebase_token = firebase_sign_in(firebase_api_key, email, password)
    except Exception as e:
        log("ERROR", f"Firebase sign-in failed: {e}")
        return 1

    # ── Exchange for stream token ──
    try:
        stream_token, expires_at = exchange_stream_token(
            base_url, firebase_token, timeout_s, ssl_context
        )
        if expires_at:
            remaining = max(0, int(expires_at) - int(time.time()))
            log("INFO", f"Stream token TTL: {remaining}s ({remaining//3600}h {(remaining%3600)//60}m)")
    except Exception as e:
        log("ERROR", f"Token exchange failed: {e}")
        return 1

    # ── Probe all media sizes ──
    print(f"\n{hr('═')}")
    print("  PROBING MEDIA SIZES")
    print(hr("═"))

    media_infos: Dict[int, MediaInfo] = {}
    for mid in message_ids:
        try:
            info = probe_media_size(base_url, mid, stream_token, timeout_s, ssl_context)
            media_infos[mid] = info
            size_text = format_bytes(info.total_size) if info.total_size else "unknown"
            log("PROBE", f"message_id={mid}  size={size_text}  type={info.content_type}")
        except Exception as e:
            log("WARN", f"Probe failed for message_id={mid}: {e}")
            media_infos[mid] = MediaInfo(message_id=mid)

    # ── Run tests ──
    all_results: List[StreamTestResult] = []

    # Test 1: Sequential (baseline)
    if "sequential" in test_names:
        print(f"\n{hr('═')}")
        print(f"  TEST 1: SEQUENTIAL (one request at a time, first {format_bytes(chunk_size)} of each media)")
        print(hr("═"))

        seq_results = run_sequential_test(
            base_url, message_ids, stream_token,
            chunk_size, timeout_s, ssl_context,
        )
        all_results.extend(seq_results)

    # Test 2: Concurrent burst
    if "burst" in test_names:
        for round_num in range(1, rounds + 1):
            round_label = f"burst_r{round_num}" if rounds > 1 else "burst"
            print(f"\n{hr('═')}")
            print(
                f"  TEST 2: CONCURRENT BURST (all {len(message_ids)} media at once, "
                f"concurrency={concurrency})"
                + (f"  round {round_num}/{rounds}" if rounds > 1 else "")
            )
            print(hr("═"))

            burst_results = run_concurrent_burst(
                base_url, message_ids, stream_token,
                chunk_size, concurrency, timeout_s, ssl_context,
                label=round_label,
            )
            all_results.extend(burst_results)

            if round_num < rounds:
                time.sleep(1)  # brief pause between rounds

    # Test 3: Multi-chunk concurrent
    if "multi_chunk" in test_names:
        print(f"\n{hr('═')}")
        print(
            f"  TEST 3: MULTI-CHUNK ({num_chunks} chunks × {len(message_ids)} media, "
            f"concurrency={concurrency})"
        )
        print(hr("═"))

        mc_results = run_concurrent_multi_chunk(
            base_url, message_ids, stream_token,
            chunk_size, num_chunks, concurrency,
            timeout_s, ssl_context, media_infos,
        )
        all_results.extend(mc_results)

    # Test 4: Same media hammered
    if "same_media" in test_names:
        target_mid = same_media_target_id
        target_info = media_infos.get(target_mid)
        total = target_info.total_size if target_info else None
        print(f"\n{hr('═')}")
        print(
            f"  TEST 4: SAME MEDIA HAMMER (message_id={target_mid}, "
            f"{same_media_requests} concurrent requests at different offsets)"
        )
        print(hr("═"))

        sm_results = run_same_media_concurrent(
            base_url, target_mid, stream_token,
            chunk_size, same_media_requests, concurrency,
            timeout_s, ssl_context, total,
        )
        all_results.extend(sm_results)

    # Test 5: Ramp-up (concurrency scaling)
    if "ramp_up" in test_names:
        print(f"\n{hr('═')}")
        print(
            f"  TEST 5: RAMP-UP (gradually increase concurrency 1→{concurrency}, "
            f"using {len(message_ids)} media)"
        )
        print(hr('═'))

        ramp_results = run_ramp_up(
            base_url, message_ids, stream_token,
            chunk_size, concurrency, timeout_s, ssl_context,
        )
        all_results.extend(ramp_results)

    # ── Summary ──
    print_summary(all_results, media_infos)

    # ── Save results to JSON (auto sequential path) ──
    save_path = make_results_path(base_url)
    save_data = {
        "timestamp": datetime.now().isoformat(),
        "base_url": base_url,
        "chunk_size": chunk_size,
        "concurrency": concurrency,
        "num_messages": len(message_ids),
        "message_ids": message_ids,
        "tests_run": test_names,
        "test_profile": {
            "fixed": True,
            "chunk_size": chunk_size,
            "concurrency": concurrency,
            "rounds": rounds,
            "num_chunks": num_chunks,
            "same_media_requests": same_media_requests,
            "timeout_seconds": timeout_s,
            "same_media_id": same_media_target_id,
            "tests": test_names,
        },
        "media_info": {
            str(mid): {
                "total_size": info.total_size,
                "content_type": info.content_type,
            } for mid, info in media_infos.items()
        },
        "results": [
            {
                "message_id": r.message_id,
                "test_label": r.test_label,
                "range_spec": r.range_spec,
                "status": r.status,
                "bytes_read": r.bytes_read,
                "expected_bytes": r.expected_bytes,
                "elapsed_s": round(r.elapsed_s, 4),
                "ttfb_s": round(r.ttfb_s, 4) if r.ttfb_s else None,
                "throughput_mbps": round(r.throughput_mbps, 4),
                "error": r.error,
            }
            for r in all_results
        ],
    }
    with open(save_path, "w", encoding="utf-8") as f:
        json.dump(save_data, f, indent=2, ensure_ascii=False)
    log("SAVE", f"Results saved to {save_path}")

    failed_count = sum(1 for r in all_results if r.status not in (200, 206) or r.error is not None)
    return 0 if failed_count == 0 else 1


if __name__ == "__main__":
    sys.exit(main())

