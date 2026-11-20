# Spec Delta

## ADDED Requirements

### Requirement: Framework-neutral panic response
The library SHALL turn a recovered panic and a request description into a complete safe-500 response without writing it, so a framework adapter answers panics exactly as the net/http recovery middleware does. It SHALL report that nothing must be written when the panic shows the client connection is broken. It SHALL walk the panic value once and report the panic to the hooks once.

#### Scenario: Panic response without writing
- **Given** a handler with a recording hook
- **When** an adapter asks for the panic response to `GET /v1/vessels/7` for a panic with `<value>`
- **Then** the result is `<result>`
- **And** the hook received `<hook>`

##### Examples:
| value | result | hook |
|-------|--------|------|
| `"boom"` | write: status 500, `Content-Type: application/problem+json`, body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error holding the value, with the 500 problem |
| `fmt.Errorf("write: %w", syscall.EPIPE)` | do not write | a panic error holding the value, with a nil problem |
| `syscall.ECONNRESET` | do not write | a panic error holding the value, with a nil problem |
| an error whose `Unwrap() error` returns itself | write: status 500, the same body | an `ErrUnwalkableError` note with reason `budget`, then a panic error holding the value |
| an error whose `Is` method panics with `"boom"` | write: status 500, the same body | an `ErrUnwalkableError` note with reason `panic`, then a panic error holding the value |
| `nil` panic value recovered under `GODEBUG=panicnil=1` | write: status 500, the same body | a panic error with value `nil` |
| a nil `*PanicError` passed by the adapter | write: status 500, the same body | a panic error with value `nil` |

### Requirement: Nested recovery never loses a panic
Nested recovery middleware SHALL report a panic that stops at the innermost recovery once. Every outer recovery SHALL report a handler that did not return, with a nil problem when the response has started, so a second real panic is never lost. A `runtime.Goexit`, which no `recover` stops and which an outer recovery cannot tell from a new `panic(nil)` under `GODEBUG=panicnil=1`, is therefore reported by each recovery.

#### Scenario: Nested recovery
- **Given** a handler with a recording hook, and its recovery middleware wrapping its recovery middleware around a handler that `<does>`
- **When** a client sends `GET /v1/vessels/7`
- **Then** within 1 second the client receives status 500 with the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the hook received exactly `<hook>`

##### Examples:
| does | hook |
|------|------|
| panics with `"boom"` | one panic error with value `"boom"`, with the 500 problem |
| calls `runtime.Goexit` | a panic error with value `nil` with the 500 problem, then a panic error with value `nil` with a nil problem |
| calls `runtime.Goexit`, with a middleware between the two recoveries that wraps the response writer and unwraps to it | a panic error with value `nil` with the 500 problem, then a panic error with value `nil` with a nil problem |

#### Scenario: A second panic(nil) after nested recovery
- **Given** a binary run with `GODEBUG=panicnil=1`, a handler with a recording hook, and its recovery middleware around a middleware that calls the inner recovery middleware, around a handler that calls `panic(nil)`, and then calls `panic(nil)` itself once the inner one has returned
- **When** a client sends `GET /v1/vessels/7`
- **Then** the client receives status 500 with the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the hook received exactly a panic error with value `nil` with the 500 problem, then a panic error with value `nil` with a nil problem

#### Scenario: Goexit inside an error method
- **Given** a handler with a recording hook, whose recovery middleware wraps a handler returning an error whose `Unwrap` calls `runtime.Goexit`
- **When** a client sends `GET /v1/vessels/7`
- **Then** the client receives status 500 with the body `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`
- **And** the hook received exactly one panic error with value `nil`, and no `ErrUnwalkableError` note: the goroutine exits before the aborted walk can be reported

## MODIFIED Requirements

### Requirement: Panic values with panicking methods are answered
When the recovered panic value is an error whose chain cannot be walked, because its own methods panic or because it loops back on itself, the recovery middleware SHALL answer like any other panic. The client SHALL receive the safe 500 problem, and no panic SHALL escape the middleware. The hook SHALL receive one `ErrUnwalkableError` note naming the reason, then the original panic.

#### Scenario: Hostile panic value
- **Given** a handler wrapped by the recovery middleware that panics with `<value>`
- **When** a client sends a request
- **Then** within 1 second the client receives status `<status>` with `Content-Type: <type>` and the body `<body>`
- **And** the body does not contain `boom`
- **And** the hook received `<hook>`

##### Examples:
| value | status | type | body | hook |
|-------|--------|------|------|------|
| an error whose `Is` method panics with `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `panic` wrapping a panic error with value `"boom"`, then a panic error holding the value |
| an error whose `Unwrap() error` returns itself | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `budget`, then a panic error holding the value |
| an `errors.Join` whose entries contain the join | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | an `ErrUnwalkableError` note with reason `budget`, then a panic error holding the value |
| an error wrapping `syscall.EPIPE` | (no response written) | (none) | (none) | a panic error holding the value |
| a typed-nil `*MyError` whose `Is` method has a value receiver | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error holding the value |
| `"boom"` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error holding the value |
| `nil`, the handler calling `panic(nil)` in a binary run with `GODEBUG=panicnil=1` | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error with value `nil` |
| none: the handler calls `runtime.Goexit`, served by a real net/http server over HTTP/1.1 | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | a panic error with value `nil` |
| none: the handler calls `runtime.Goexit`, served by a real net/http server over HTTP/2 | 500 | `application/problem+json` | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}`, then a stream reset with `INTERNAL_ERROR`, which net/http's HTTP/2 server sends for a handler that did not return | a panic error with value `nil`, once |

#### Scenario: Hostile panic value without a hook
- **Given** a handler wrapped by the recovery middleware of a handler with no hook, that panics with `<value>`
- **When** a client sends a request
- **Then** the client receives status `<status>`
- **And** the server keeps serving

##### Examples:
| value | status |
|-------|--------|
| an error whose `Is` method panics with `"boom"` | 500 |
| an error whose `Unwrap() error` returns itself | 500 |
