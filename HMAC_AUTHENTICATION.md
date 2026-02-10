# HMAC Authentication (Deprecated)

HMAC authentication for `/direct/:messageID` has been removed.

Current authentication flow is Firebase-only:

1. Client sends Firebase ID token to `POST /auth/firebase/exchange`.
2. Stream server returns a short-lived `stream_token`.
3. Client uses this token in `/direct/:messageID` (`st` query, `x-stream-token` header, bearer short token, or exchange cookie).

For setup details, see `FIREBASE_STREAM_AUTHENTICATION.md`.
