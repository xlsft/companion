# STT sidecar protocol

- Tickets: OC-28 (protocol), OC-29 (whisper.cpp runtime)
- Code: `internal/stt/protocol` (wire types, codec), `internal/stt` (core client), `internal/stt/server` + `cmd/stt-sidecar` (sidecar)
- Protocol version: `1` (returned by `initialize`; bump on incompatible change)

## Transport

JSON-RPC 2.0 over the sidecar's stdin (core → sidecar) and stdout (sidecar → core), one JSON object per line (`\n`-terminated, UTF-8, max 8 MiB per line). No batch requests. stderr is a technical log only and never contains transcript text or audio (ADR-0001).

- Requests carry an `id` (the core uses increasing integers) and get exactly one response.
- Notifications have no `id` and get no response.
- A malformed line is answered with `-32700 parse error` (`id: null`); the stream stays open. A line over the size limit closes the stream.
- EOF on stdin is treated as `shutdown`.

Audio is PCM signed 16-bit little-endian, mono, 16 kHz, base64 inside `session.audio`. The sidecar never resamples; the core's audio pipeline does (OC-27).

## Lifecycle

```
core                                   sidecar
 |-- initialize ----------------------->|  load model, pick backend
 |<--------------------- result --------|
 |-- session.start {s1} --------------->|
 |<--------------------- result --------|
 |-- session.audio {s1, seq 0} -------->|  (notification, every ~20–100 ms)
 |<-------------- session.vad {speech} -|
 |-- session.audio {s1, seq 1} -------->|
 |<----------- session.partial {text} --|  every ~700 ms of new audio
 |-- session.finish {s1} -------------->|  final decode over whole utterance
 |<---------------- result {final} -----|
 |-- shutdown ------------------------->|
 |<--------------------- result --------|  process exits 0
```

## Methods

### `initialize` (request)

Must be the first request; any session method before it fails with `-32006`. A second call fails with `-32007`.

| Param | Type | Notes |
|---|---|---|
| `model_path` | string | ggml model file. Required by the whisper engine; ignored by `fake`. |
| `model_sha256` | string, optional | Hex SHA-256; verified before load. |
| `language` | string | `"auto"` or an ISO 639-1 code (`"en"`, `"ru"`, …). Default for all sessions. |
| `backend` | string | `"cpu"` or `"auto"` (use a GPU backend when the build has one and a device exists). |
| `threads` | int, optional | Decode threads; `0` = engine default (min(NumCPU, 8)). |

Result:

| Field | Type | Notes |
|---|---|---|
| `protocol_version` | string | `"1"` |
| `engine` | string | `"whisper"` or `"fake"` |
| `runtime_version` | string | e.g. whisper.cpp version |
| `backend_in_use` | string | `"cpu"` or the ggml device name |
| `fallback_reason` | string, optional | Set when `auto` could not start a GPU backend and CPU was used instead (`"backend_init_failed"`). Initialization still succeeds; a `log{warn, backend_fallback_cpu}` is also emitted. |
| `languages` | string[] | Languages the model supports (`["en"]` for `.en` models). |

### `session.start` (request)

| Param | Type | Notes |
|---|---|---|
| `session_id` | string | Chosen by the core, unique per sidecar process. Restarting a Talk uses a new id, so late events of the old one can be dropped by id. |
| `sample_rate` | int | Must be `16000`, else `-32009`. |
| `format` | string | Must be `"pcm_s16le"`, else `-32009`. |
| `partials` | bool | Emit `session.partial` while audio streams. |
| `language` | string, optional | Overrides the `initialize` language. |

Result: `{}`. Duplicate id → `-32008`.

### `session.audio` (notification, core → sidecar)

`{session_id, seq, data}`. `seq` starts at 0 and increases by one per chunk. Chunks for unknown, finishing or cancelled sessions are dropped silently (a `log{debug, audio_dropped}` is emitted), because the core may race a chunk with cancel. Chunks with `seq` lower than or equal to an already received one are dropped (`log{warn, audio_seq_out_of_order}`); a gap is logged (`audio_seq_gap`) but the audio is kept. Audio beyond the per-session limit (default 5 min) is dropped (`audio_overflow`).

### `session.finish` (request)

`{session_id}`. Stops accepting audio, cancels any in-flight partial decode and decodes the whole utterance. Result:

| Field | Type | Notes |
|---|---|---|
| `session_id` | string | |
| `text` | string | Trimmed full transcript. |
| `language` | string | Detected language when `auto`, otherwise the requested one. |
| `segments` | `[{start_ms, end_ms, text}]` | Relative to session start. |
| `duration_ms` | int | Audio duration received. |
| `decode_ms` | int | Final decode wall time (diagnostics). |
| `speech` | bool | `false` when VAD heard no speech; the decode is then skipped and `text` is empty, so silence never turns into a hallucinated transcript. |

The session is removed when the response is sent. If `session.cancel` arrives during the decode the response is `-32005 session cancelled`.

### `session.cancel` (request)

`{session_id}` → `{}`. Aborts any decode for the session (whisper.cpp abort callback) and forgets it; no further notifications for that id are sent. Unknown id → `-32004` (the core ignores it: the session may have just finished).

### `session.partial` (notification, sidecar → core)

`{session_id, seq, text, stable}`. Produced by the rolling decoder: every `partial-interval` (default 700 ms) of new audio, once VAD has heard speech, the last `partial-window` (default 10 s) of audio is decoded with a cheaper single-segment setting. At most one partial decode runs per session; while one runs, new audio keeps accumulating and the next decode starts with the first chunk after it completes, so a slow engine lowers the partial rate instead of queueing work. `stable` is reserved for a future agreement-based stabiliser and is currently always `false`; every partial replaces the previous one. No partials are sent after `session.finish` or `session.cancel` was received.

### `session.vad` (notification, sidecar → core)

`{session_id, speech, at_ms}` on every speech/silence transition of the sidecar VAD (energy-based, `internal/stt/vad`).

### `log` (notification, sidecar → core)

`{level, code, session_id?}`. `level` is `debug|info|warn|error`; `code` is a stable snake_case identifier. Never free text, never transcript content.

| Code | Level | Meaning |
|---|---|---|
| `backend_fallback_cpu` | warn | GPU backend failed to start, running on CPU |
| `audio_dropped` | debug | audio for an unknown/finished/cancelled session |
| `audio_seq_out_of_order` | warn | duplicate or old `seq` |
| `audio_seq_gap` | warn | `seq` skipped ahead |
| `audio_overflow` | warn | per-session audio limit reached |
| `partial_failed` | warn | a partial decode failed (session continues) |

### `health` (request)

`{}` → `{ok, initialized, uptime_ms, sessions}`. Answered even while decodes run; used by the supervisor (OC-36).

### `shutdown` (request)

`{}` → `{}`. Cancels all sessions, releases the model and exits with status 0 after the response is flushed.

## Errors

| Code | Name | When |
|---|---|---|
| -32700 | parse error | line is not JSON / too long |
| -32600 | invalid request | not a JSON-RPC 2.0 object |
| -32601 | method not found | unknown request method |
| -32602 | invalid params | params do not decode / required field missing |
| -32603 | internal error | unexpected sidecar failure |
| -32001 | model not found | `model_path` missing or unreadable |
| -32002 | model checksum mismatch | `model_sha256` does not match |
| -32003 | backend init failed | no backend at all could start (CPU fallback also failed) or the model is invalid |
| -32004 | session unknown | id was never started, already finished or cancelled |
| -32005 | session cancelled | pending `session.finish` aborted by `session.cancel` |
| -32006 | not initialized | session method before `initialize` |
| -32007 | already initialized | second `initialize` |
| -32008 | session exists | duplicate `session_id` |
| -32009 | unsupported audio format | `sample_rate`/`format` not 16000/`pcm_s16le` |
| -32010 | transcribe failed | engine returned an error on final decode |
| -32011 | shutting down | request after `shutdown` |

`error.data` may hold a technical detail (field name, errno class); never user content.

## Client semantics (`internal/stt`)

- `stt.Start(ctx, cfg)` spawns the sidecar with `exec.CommandContext` and runs `initialize`; `stt.NewClient(r, w)` speaks the protocol over any pipe pair (tests, in-process).
- `Client.StartSession(ctx, opts...)` → `*Session` with `Write([]int16)`, `Partials() <-chan Partial`, `Speech() <-chan bool`, `Finish(ctx) (Final, error)`, `Cancel()`.
- After `Cancel()` or `Finish` returns, late notifications for that session are dropped and the channels are closed.
- `Partials()` keeps only the newest partials if the consumer falls behind (each partial supersedes the previous one).
- If the sidecar exits or its stdout breaks, every pending call fails with `stt.ErrSidecarExited` (wrapping the exit status) and all sessions end. Restarting is the supervisor's job (OC-36).

## Sidecar flags

| Flag | Default | |
|---|---|---|
| `-engine` | `whisper` if built with `-tags whisper`, else `fake` | `fake` is deterministic, for tests |
| `-partial-interval` | `700ms` | new audio between partial decodes |
| `-partial-window` | `10s` | audio decoded per partial |
| `-max-utterance` | `5m` | per-session audio limit |
| `-vad-threshold-db` | `-45` | frame RMS in dBFS counted as speech |
| `-vad-hangover` | `400ms` | silence before speech→silence transition |
| `-vad` | `true` | `false` treats all audio as speech |

## whisper.cpp build

`make whisper` builds whisper.cpp from the `third_party/whisper.cpp` submodule (pinned release tag) as static libraries into `third_party/whisper-install/`, then `make build-whisper` builds `bin/stt-sidecar` with `-tags whisper`. Without the tag the sidecar is pure Go and only offers the `fake` engine; `make cross` never uses the tag.

CMake flags always set: `-DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DWHISPER_BUILD_EXAMPLES=OFF -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF`, plus `-DGGML_NATIVE=$(WHISPER_NATIVE)` (default `ON`: tuned to the build host's CPU; set `WHISPER_NATIVE=OFF` for redistributable binaries). GPU/BLAS backends are enabled only when detected on the build host:

| Backend | Detected by | CMake flag | Extra link flags |
|---|---|---|---|
| Vulkan | `pkg-config vulkan`, `vulkan/vulkan.h` and `glslc` | `-DGGML_VULKAN=ON` | `-lggml-vulkan -lvulkan` |
| CUDA | `nvcc` on `PATH` | `-DGGML_CUDA=ON` | `-lggml-cuda -lcudart -lcublas -lcublasLt -lcuda` |
| OpenBLAS | `pkg-config openblas` | `-DGGML_BLAS=ON -DGGML_BLAS_VENDOR=OpenBLAS` | `-lggml-blas -lopenblas` |
| Metal | macOS (ggml default) | — | frameworks in `whisper_darwin.go` |

Override detection with `WHISPER_CMAKE_EXTRA=...` and `WHISPER_CGO_LDFLAGS=...`. Silero VAD (whisper.cpp `whisper_vad_*`) is not used yet; `internal/stt/vad.VAD` is the hook for it.

The cgo code lives in `internal/stt/engine/whisper` (build tag `whisper && cgo`) and is imported only by `cmd/stt-sidecar/engine_whisper.go`, so no core binary links it. ADR-0001 rule 6 names only `internal/platform/*` and `cmd/stt-sidecar` as cgo locations; this package is the sidecar's private runtime and should be added to that rule.

Tests: `make test-whisper` runs the tagged tests; the model-dependent ones run only when `OPENCLICK_WHISPER_MODEL` points at a ggml model (for example `ggml-tiny.bin` from the `ggerganov/whisper.cpp` Hugging Face repo) and use the submodule's `samples/jfk.wav`. Models and audio are never committed.
