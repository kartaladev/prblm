# Spec Delta

## MODIFIED Requirements

### Requirement: Client disconnects are not answered
When the request context is canceled and the last error wraps `context.Canceled`, the middleware SHALL write nothing and SHALL still call the hook. The check SHALL use the bounded walk, which SHALL report nothing: an error whose chain the check cannot finish SHALL count as coming from a client still there, and SHALL be answered as the core resolves it: when the core's walk cannot finish either, with the safe 500 and an `ErrUnwalkableError` note; otherwise as usual, with no note.

#### Scenario: Client went away
- **Given** a gin engine using the middleware and a request whose context is `<ctx>`
- **When** the handler records `<error>`
- **Then** within 1 second the response is `<response>`
- **And** the hook was called `<calls>` times

##### Examples:
| ctx | error | response | calls |
|-----|-------|----------|-------|
| canceled | `fmt.Errorf("query: %w", context.Canceled)` | not written by the library | 1 |
| active | `context.Canceled` | a 500 problem | 1 |
| canceled | an error whose `Unwrap() error` returns itself | a 500 problem | 2 |
| canceled | an error whose `Is` method panics with `"boom"` | a 500 problem | 2 |
| canceled | an error wrapping `prblm.ErrNotFound` whose `Is` method panics only when asked for `context.Canceled` | a 404 problem | 1 |

### Requirement: Recovery answers panic values with panicking methods
When a handler panics with an error whose chain cannot be walked, because its own methods panic or because it loops back on itself, the gin recovery middleware SHALL answer like any other panic. The client SHALL receive the core's safe 500 problem, and no panic SHALL escape to gin or net/http. The hooks SHALL receive one `ErrUnwalkableError` note, then the panic. The panic value SHALL be walked once, as the net/http recovery middleware walks it.

#### Scenario: Hostile panic value under gin
- **Given** a gin engine with `<chain>` and a handler that panics with `<value>`
- **When** a client sends a request
- **Then** within 1 second the client receives status `<status>` with `Content-Type: <type>` and the body `<body>`
- **And** the body does not contain `boom`
- **And** the hook received `<hook>`

##### Examples:
| chain | value | status | type | body | hook |
|-------|-------|--------|------|------|------|
| recovery, then middleware | an error whose `Is` method panics with `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `panic` wrapping a panic error with value `"boom"`, then an error that unwraps to a panic error holding the value |
| recovery only | an error whose `Is` method panics with `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `panic` wrapping a panic error with value `"boom"`, then an error that unwraps to a panic error holding the value |
| recovery only | an error whose `Unwrap() error` returns itself | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `budget`, then an error that unwraps to a panic error holding the value |
| recovery, then middleware | an `errors.Join` whose entries contain the join | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `budget`, then an error that unwraps to a panic error holding the value |
| recovery, then middleware | an error wrapping `syscall.EPIPE` | (no response written) | (none) | (none) | an error that unwraps to a panic error holding the value |
| recovery only | `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an error that unwraps to a panic error holding the value |
| recovery only | `nil`, the handler calling `panic(nil)` in a binary run with `GODEBUG=panicnil=1` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an error that unwraps to a panic error with value `nil` |
| recovery only, over a real server (HTTP/1.1) | none: the handler calls `runtime.Goexit` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an error that unwraps to a panic error with value `nil` |
| recovery, then middleware, over a real server (HTTP/1.1) | none: the handler calls `runtime.Goexit` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an error that unwraps to a panic error with value `nil` |
| middleware, then recovery, over a real server (HTTP/1.1) | none: the handler calls `runtime.Goexit` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an error that unwraps to a panic error with value `nil` |
| recovery only, over a real server (HTTP/2) | none: the handler calls `runtime.Goexit` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`, then a stream reset with `INTERNAL_ERROR`, which net/http's HTTP/2 server sends for a handler that did not return | an error that unwraps to a panic error with value `nil`, once |

## ADDED Requirements

### Requirement: Nested recovery never loses a panic
When the gin recovery middleware is nested with itself or with the core's net/http recovery middleware, a panic that stops at the innermost recovery SHALL be answered by it and reported once. Every outer recovery SHALL report a handler that did not return, so a second real panic is never lost. A `runtime.Goexit`, which an outer recovery cannot tell from a new `panic(nil)` under `GODEBUG=panicnil=1`, is therefore reported by each recovery.

#### Scenario: Nested gin recovery
- **Given** `<chain>` with a recording hook, and a handler that `<does>`
- **When** a client sends `GET /v1/vessels/7`
- **Then** within 1 second the client receives status 500 with the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the hook received exactly `<hook>`

##### Examples:
| chain | does | hook |
|-------|------|------|
| a gin engine using the recovery middleware twice | calls `runtime.Goexit` | two errors that unwrap to a panic error with value `nil`, each with the 500 problem (the outer one's unsent) |
| a gin engine using the recovery middleware twice | panics with `"boom"` | one error that unwraps to a panic error with value `"boom"` |
| the core's recovery middleware around a gin engine using the recovery middleware | calls `runtime.Goexit` | an error that unwraps to a panic error with value `nil` with the 500 problem, then a panic error with value `nil` with a nil problem |
| the core's recovery middleware around a gin engine using the recovery middleware | panics with `"boom"` | one error that unwraps to a panic error with value `"boom"` |
| a gin engine using the recovery middleware, with the core's recovery middleware inside the handler | calls `runtime.Goexit` | two errors that unwrap to a panic error with value `nil` |

#### Scenario: A second panic(nil) after nested gin recovery
- **Given** a binary run with `GODEBUG=panicnil=1` and a gin engine using the recovery middleware, then a middleware that calls `panic(nil)` once the handlers after it have returned, then the recovery middleware again, with a handler that calls `panic(nil)`
- **When** a client sends `GET /v1/vessels/7`
- **Then** the hook received exactly two errors that unwrap to a panic error with value `nil`: the handler's, then the middleware's

### Requirement: The middleware answers a panicking Respond with a bare 500
Should resolving an error panic, which the core's containment leaves possible only for a request context whose `Value` method panics, the middleware SHALL answer with a bare 500 without a body, and no panic SHALL escape to gin or net/http. It SHALL tell a panic apart by the call not returning, so a `panic(nil)` under `GODEBUG=panicnil=1` gets the bare 500 too.

#### Scenario: Respond panics
- **Given** a gin engine using the middleware, a middleware that replaces the request context with one whose `Value` method panics with `<value>`, and a handler that records `errors.New("db down")`
- **When** a client sends `GET /v1/vessels/7`
- **Then** the client receives status 500

##### Examples:
| value |
|-------|
| `"boom"` |
| `nil`, in a binary run with `GODEBUG=panicnil=1` |
