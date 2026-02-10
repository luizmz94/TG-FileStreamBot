# Firebase Stream Authentication

This project now supports a high-performance auth flow for `/direct/:message_id`:

1. The client authenticates with Firebase (normal app login).
2. The client sends the Firebase ID token once to:
   - `POST /auth/firebase/exchange`
   - Header: `Authorization: Bearer <firebase_id_token>`
3. The stream server validates Firebase JWT and returns a short-lived `stream_token`.
4. All subsequent `/direct/:message_id` requests use only the local `stream_token`:
   - Query: `?st=<stream_token>`
   - Header: `x-stream-token: <stream_token>`
   - Header: `Authorization: Bearer <stream_token>`
   - Or HttpOnly cookie set by exchange endpoint.

No Firebase verification happens on `/direct` requests.

## Environment variables

```env
FIREBASE_PROJECT_ID=mediatg-16cbb
FIREBASE_CERTS_URL=https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com
STREAM_SESSION_TTL_SECONDS=28800
STREAM_SESSION_CLEANUP_SECONDS=60
STREAM_SESSION_COOKIE_NAME=fsb_stream_session
STREAM_SESSION_COOKIE_SECURE=true
STREAM_SESSION_COOKIE_DOMAIN=
```

`/direct` now accepts only Firebase-exchanged stream session tokens (`st`, `x-stream-token`, bearer short token, or cookie).

## Quick test

```bash
# 1) Exchange Firebase token
curl -X POST "https://your-stream-host/auth/firebase/exchange" \
  -H "Authorization: Bearer <firebase_id_token>"

# Response:
# {
#   "stream_token": "...",
#   "expires_at": 1739333282,
#   ...
# }

# 2) Stream using local token
curl "https://your-stream-host/direct/123?st=<stream_token>"
```

## Notes

- Session tokens are stored in-memory (fast, O(1) lookup).
- Restarting the service invalidates active stream sessions.
- For horizontal scaling, use a shared store (e.g. Redis) for session tokens.
- In Docker production with a `scratch` runtime, CA certificates must be present or Firebase exchange can fail with:
  `x509: certificate signed by unknown authority`.
